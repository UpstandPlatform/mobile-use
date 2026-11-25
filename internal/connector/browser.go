package connector

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"mobile-use/internal/wire"
)

const documentExpression = `(() => [String(performance.timeOrigin), location.href])()`

const pageExpression = `(() => ({
  title: document.title.slice(0, 256),
  url: location.href.slice(0, 2048),
  elements: Array.from(document.querySelectorAll('a,button,input,textarea,select,[role],h1,h2,h3,label'))
    .filter(el => el.getClientRects().length > 0).slice(0, 350).map(el => ({
      tag: el.tagName.toLowerCase(), id: (el.id || '').slice(0, 160),
      role: (el.getAttribute('role') || '').slice(0, 80),
      text: (el.type === 'password' || el.matches('input,textarea') ? '' : (el.textContent || '')).trim().slice(0, 256),
      label: (el.getAttribute('aria-label') || '').slice(0, 256),
      type: (el.getAttribute('type') || '').slice(0, 48)
    }))
}))()`

func actionExpression(op wire.BrowserRequest) string {
	// Values are encoded as JSON arguments to a static script. The caller cannot supply JavaScript.
	return `((request) => {
  if (request.type === 'scroll') { window.scrollBy(0, request.delta_y); return 'ok'; }
  let element;
  try { element = document.querySelector(request.selector); } catch (_) { return 'selector'; }
  if (!element || !element.getClientRects().length || element.disabled) return 'missing';
  if (element.type === 'password' || element.autocomplete === 'one-time-code') return 'protected';
  if (request.type === 'click') { element.click(); return 'ok'; }
  if (request.type === 'fill') {
    const validInput = element instanceof HTMLInputElement && /^(text|search|tel|url|email|number)$/.test(element.type);
    if ((!validInput && !(element instanceof HTMLTextAreaElement)) || element.readOnly) return 'unsupported';
    const prototype = validInput ? HTMLInputElement.prototype : HTMLTextAreaElement.prototype;
    Object.getOwnPropertyDescriptor(prototype, 'value').set.call(element, request.text || '');
    element.dispatchEvent(new Event('input', {bubbles: true}));
    element.dispatchEvent(new Event('change', {bubbles: true}));
    return 'ok';
  }
  return 'unsupported';
})(` + wire.Marshal(op) + `)`
}

func browserActionError(result string) error {
	switch result {
	case "ok":
		return nil
	case "selector":
		return wire.Error("INVALID_SELECTOR", "Provide a valid CSS selector.", 422)
	case "missing":
		return wire.ErrStale
	case "protected":
		return wire.ErrLocked
	default:
		return wire.ErrUnsupported
	}
}

func documentID(contextID, key string) string {
	hash := sha256.Sum256([]byte(contextID + "\x00" + key))
	return hex.EncodeToString(hash[:16])
}

func publishedURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" && u.Scheme != "https" {
		return ""
	}
	u.User, u.Fragment, u.RawQuery, u.ForceQuery = nil, "", "", false
	return boundedText(u.String(), 1024)
}

type cdpBrowser struct {
	device  *androidDevice
	ports   map[string]int
	targets map[string]cdpTarget
}

type cdpTarget struct {
	ID        string `json:"id"`
	Type      string `json:"type"`
	Title     string `json:"title"`
	URL       string `json:"url"`
	WebSocket string `json:"webSocketDebuggerUrl"`
	port      int
}

var debugSocket = regexp.MustCompile(`^webview_devtools_remote_[0-9]+$`)

func (a *androidDevice) Browser(ctx context.Context, op wire.Operation) (wire.Result, error) {
	if a.browser == nil {
		a.browser = &cdpBrowser{device: a, ports: make(map[string]int)}
	}
	return a.browser.perform(ctx, op)
}

func (b *cdpBrowser) close(ctx context.Context) {
	for _, port := range b.ports {
		_, _ = b.device.run(ctx, "forward", "--remove", "tcp:"+strconv.Itoa(port))
	}
	clear(b.ports)
}

var noProxyHTTP = &http.Client{
	Transport: &http.Transport{}, Timeout: 2 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

func (b *cdpBrowser) discover(ctx context.Context) ([]wire.BrowserContext, error) {
	sockets := []string{"chrome_devtools_remote"}
	if display, err := b.device.Display(ctx); err == nil && packageMatch.MatchString(display.AppID) {
		if pids, err := b.device.shell(ctx, "pidof "+display.AppID); err == nil {
			for _, pid := range strings.Fields(pids) {
				candidate := "webview_devtools_remote_" + pid
				if debugSocket.MatchString(candidate) && len(sockets) < 6 {
					sockets = append(sockets, candidate)
				}
			}
		}
	}
	if output, err := b.device.shell(ctx, "cat /proc/net/unix"); err == nil {
		for _, entry := range strings.Fields(output) {
			if socket, ok := strings.CutPrefix(entry, "@"); ok && debugSocket.MatchString(socket) && len(sockets) < 6 && !slices.Contains(sockets, socket) {
				sockets = append(sockets, socket)
			}
		}
	}
	for socket, port := range b.ports {
		if socket == "chrome_devtools_remote" {
			continue
		}
		active := false
		for _, available := range sockets {
			if socket == available {
				active = true
				break
			}
		}
		if !active {
			_, _ = b.device.run(ctx, "forward", "--remove", "tcp:"+strconv.Itoa(port))
			delete(b.ports, socket)
		}
	}
	b.targets = make(map[string]cdpTarget)
	result := []wire.BrowserContext{}
	for _, socket := range sockets {
		port := b.ports[socket]
		if port == 0 {
			output, err := b.device.run(ctx, "forward", "tcp:0", "localabstract:"+socket)
			if err != nil {
				continue
			}
			port, _ = strconv.Atoi(strings.TrimSpace(string(output)))
			if port < 1 || port > 65535 {
				continue
			}
			b.ports[socket] = port
		}
		targets, err := listCDPTargets(ctx, port)
		if err != nil {
			continue
		}
		for _, target := range targets {
			if len(result) >= 100 {
				break
			}
			if target.Type != "page" && target.Type != "webview" {
				continue
			}
			if target.ID == "" || target.WebSocket == "" {
				continue
			}
			target.port = port
			id := documentID(socket, target.ID)
			b.targets[id] = target
			result = append(result, wire.BrowserContext{ID: id, Title: boundedText(target.Title, 256), URL: publishedURL(target.URL)})
		}
	}
	return result, nil
}

func listCDPTargets(ctx context.Context, port int) ([]cdpTarget, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("http://127.0.0.1:%d/json/list", port), nil)
	if err != nil {
		return nil, err
	}
	resp, err := noProxyHTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, wire.ErrUnsupported
	}
	var targets []cdpTarget
	err = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&targets)
	return targets, err
}

type cdpClient struct {
	socket *websocket.Conn
	id     int
}

func (c *cdpClient) call(method string, params any, result any) error {
	c.id++
	if err := c.socket.WriteJSON(map[string]any{"id": c.id, "method": method, "params": params}); err != nil {
		return err
	}
	for {
		var message struct {
			ID     int             `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  json.RawMessage `json:"error"`
		}
		if err := c.socket.ReadJSON(&message); err != nil {
			return err
		}
		if message.ID != c.id {
			continue
		}
		if len(message.Error) > 0 {
			return wire.ErrUnsupported
		}
		if result == nil {
			return nil
		}
		return json.Unmarshal(message.Result, result)
	}
}

func (c *cdpClient) evaluate(expression string, out any) error {
	var result struct {
		Result struct {
			Value json.RawMessage `json:"value"`
		} `json:"result"`
		Exception json.RawMessage `json:"exceptionDetails"`
	}
	err := c.call("Runtime.evaluate", map[string]any{"expression": expression, "returnByValue": true}, &result)
	if err != nil {
		return err
	}
	if len(result.Exception) > 0 || len(result.Result.Value) == 0 {
		return wire.ErrUnsupported
	}
	return json.Unmarshal(result.Result.Value, out)
}

func (c *cdpClient) document(contextID string) (string, error) {
	var response struct {
		FrameTree struct {
			Frame struct {
				Loader string `json:"loaderId"`
				URL    string `json:"url"`
			} `json:"frame"`
		} `json:"frameTree"`
	}
	if err := c.call("Page.getFrameTree", map[string]any{}, &response); err != nil {
		return "", err
	}
	frame := response.FrameTree.Frame
	if publishedURL(frame.URL) == "" && frame.URL != "about:blank" {
		return "", wire.ErrUnsupported
	}
	return documentID(contextID, frame.Loader+"\x00"+frame.URL), nil
}

func (b *cdpBrowser) perform(ctx context.Context, op wire.Operation) (wire.Result, error) {
	if op.Kind == "browser_contexts" {
		contexts, err := b.discover(ctx)
		return wire.Result{Contexts: contexts}, err
	}
	request := *op.Browser
	if b.targets == nil {
		if _, err := b.discover(ctx); err != nil {
			return wire.Result{}, err
		}
	}
	target, ok := b.targets[request.ContextID]
	if !ok {
		return wire.Result{}, wire.ErrStale
	}
	endpoint, err := url.Parse(target.WebSocket)
	if err != nil || !strings.HasPrefix(endpoint.Path, "/devtools/") {
		return wire.Result{}, wire.ErrUnsupported
	}
	address := url.URL{Scheme: "ws", Host: fmt.Sprintf("127.0.0.1:%d", target.port), Path: endpoint.Path}
	dialer := websocket.Dialer{HandshakeTimeout: 2 * time.Second}
	socket, response, err := dialer.DialContext(ctx, address.String(), nil)
	if err != nil {
		if response != nil && response.Body != nil {
			response.Body.Close()
		}
		return wire.Result{}, wire.ErrStale
	}
	defer socket.Close()
	stop := context.AfterFunc(ctx, func() { socket.Close() })
	defer stop()
	socket.SetReadLimit(2 << 20)
	if deadline, ok := ctx.Deadline(); ok {
		_ = socket.SetReadDeadline(deadline)
		_ = socket.SetWriteDeadline(deadline)
	}
	client := cdpClient{socket: socket}
	generation, err := client.document(request.ContextID)
	if err != nil {
		return wire.Result{}, err
	}
	if op.Kind == "browser_observe" {
		var page wire.BrowserPage
		if err := client.evaluate(pageExpression, &page); err != nil {
			return wire.Result{}, err
		}
		current, err := client.document(request.ContextID)
		if err != nil || current != generation {
			return wire.Result{}, wire.ErrStale
		}
		page.ContextID, page.DocumentID, page.URL = request.ContextID, generation, publishedURL(page.URL)
		return wire.Result{Page: &page}, nil
	}
	if generation != request.DocumentID {
		return wire.Result{}, wire.ErrStale
	}
	if request.Type == "navigate" {
		var response struct {
			ErrorText string `json:"errorText"`
		}
		if err := client.call("Page.navigate", map[string]string{"url": request.URL}, &response); err != nil || response.ErrorText != "" {
			return wire.Result{}, wire.ErrUnknown
		}
		return wire.Result{}, nil
	}
	var outcome string
	if err := client.evaluate(actionExpression(request), &outcome); err != nil {
		return wire.Result{}, wire.ErrUnknown
	}
	return wire.Result{}, browserActionError(outcome)
}
