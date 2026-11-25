import type { Device, SystemInfo } from './api';
import { CopyButton } from './AddDevicePanel';

export function McpSetup({
  devices,
  systemInfo,
  onSelectDevice,
}: {
  devices: Device[];
  systemInfo?: SystemInfo;
  onSelectDevice: (deviceId: string) => void;
}) {
  const remote = systemInfo?.mode === 'remote';
  // A remote server's binary path belongs to the server host, not the operator's
  // workstation. Remote clients use their local CLI and its saved server profile.
  const exec = remote ? 'mobile-use' : systemInfo?.mcp_executable || 'mobile-use';
  const codexCmd = remote
    ? `codex mcp add mobile-use -- ${exec} mcp`
    : systemInfo?.mcp_commands?.codex || `codex mcp add mobile-use -- ${exec} mcp`;
  const claudeCmd = remote
    ? `claude mcp add --transport stdio mobile-use -- ${exec} mcp`
    : systemInfo?.mcp_commands?.claude || `claude mcp add --transport stdio mobile-use -- ${exec} mcp`;
  const httpUrl = systemInfo?.mcp_commands?.http || `${location.origin}/mcp`;
  const remoteSetup = `mobile-use remote set --url ${systemInfo?.origin || location.origin} --token <AGENT_TOKEN>`;
  const desktopJson = JSON.stringify({
    mcpServers: {
      'mobile-use': {
        command: exec,
        args: ['mcp'],
      },
    },
  }, null, 2);

  const readyDevices = devices.filter(d => d.enabled && d.online && (d.grants?.length || 0) > 0);

  return (
    <div className="mcp-layout">
      <section className="surface mcp-summary">
        <div className="section-heading">
          <div>
            <h2>Agent &amp; MCP readiness</h2>
            <span>Connect Codex, Claude Code, Claude Desktop, Cursor, or any MCP client</span>
          </div>
        </div>
        <div className="stat-grid">
          <div className="stat-card">
            <span className="hint">Mode</span>
            <strong>{systemInfo?.mode === 'remote' ? 'Remote server' : 'Local workstation'}</strong>
            <small>{systemInfo?.origin || location.origin}</small>
          </div>
          <div className="stat-card">
            <span className="hint">MCP-ready devices</span>
            <strong>{readyDevices.length} of {devices.length}</strong>
            <small>Online, enrolled &amp; granted</small>
          </div>
          <div className="stat-card">
            <span className="hint">Wi-Fi companion</span>
            <strong>{systemInfo?.companion_enabled ? (systemInfo.companion_mode === 'lan' ? 'Active on LAN' : 'Remote HTTPS') : 'Disabled'}</strong>
            <small>{systemInfo?.companion_origin || '—'}</small>
          </div>
          <div className="stat-card">
            <span className="hint">Android ADB</span>
            <strong>{systemInfo?.adb_available ? 'Ready' : systemInfo?.mode === 'remote' ? 'Via connectors' : 'Not detected'}</strong>
            <small>{systemInfo?.wireless_adb_supported ? 'USB & Wi-Fi supported' : 'Remote connector mode'}</small>
          </div>
        </div>
      </section>

      {remote && (
        <section className="surface mcp-card">
          <div className="mcp-card-head">
            <h3>Connect a local workstation</h3>
            <CopyButton value={remoteSetup} />
          </div>
          <p className="hint">On the workstation that runs your agent, install the mobile-use CLI, replace the placeholder with an agent token for this server, and save the remote profile before registering the local MCP command.</p>
          <pre><code>{remoteSetup}</code></pre>
        </section>
      )}

      <div className="mcp-cards">
        <section className="surface mcp-card">
          <div className="mcp-card-head">
            <h3>Codex CLI</h3>
            <CopyButton value={codexCmd} />
          </div>
          <p className="hint">Register mobile-use as a stdio MCP server in OpenAI Codex CLI:</p>
          <pre><code>{codexCmd}</code></pre>
        </section>

        <section className="surface mcp-card">
          <div className="mcp-card-head">
            <h3>Claude Code</h3>
            <CopyButton value={claudeCmd} />
          </div>
          <p className="hint">Register mobile-use as a stdio MCP server in Claude Code:</p>
          <pre><code>{claudeCmd}</code></pre>
        </section>

        <section className="surface mcp-card">
          <div className="mcp-card-head">
            <h3>Claude Desktop / Cursor</h3>
            <CopyButton value={desktopJson} label="Copy JSON" />
          </div>
          <p className="hint">Add this entry to your <code>mcpServers</code> JSON configuration:</p>
          <pre><code>{desktopJson}</code></pre>
        </section>

        <section className="surface mcp-card">
          <div className="mcp-card-head">
            <h3>Streamable HTTP MCP</h3>
            <CopyButton value={httpUrl} />
          </div>
          <p className="hint">Networked or remote MCP clients connect with a bearer agent token to:</p>
          <pre><code>{httpUrl}</code></pre>
          <p className="hint">On CLI, run <code>mobile-use mcp-config</code> anytime to inspect or copy these commands.</p>
        </section>
      </div>

      <section className="surface">
        <div className="section-heading">
          <div>
            <h2>Device agent access</h2>
            <span>Devices must be enrolled with at least one agent grant before MCP tools can access them</span>
          </div>
        </div>
        {devices.length === 0 ? (
          <p className="empty">No devices in inventory yet. Add a device from the Devices tab.</p>
        ) : (
          <div className="audit">
            <table>
              <thead>
                <tr>
                  <th>Device</th>
                  <th>Transport</th>
                  <th>Status</th>
                  <th>Granted agents</th>
                  <th>Action</th>
                </tr>
              </thead>
              <tbody>
                {devices.map(d => (
                  <tr key={d.id}>
                    <td><strong>{d.label}</strong></td>
                    <td>{d.platform === 'ios' ? 'iOS' : 'Android'} · {d.mode}</td>
                    <td>{d.online ? 'Online' : 'Offline'} · {d.enabled ? d.profile : 'Unenrolled'}</td>
                    <td>{d.grants?.map(g => g.subject).join(', ') || 'None'}</td>
                    <td>
                      <button type="button" onClick={() => onSelectDevice(d.id)}>Configure</button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </section>
    </div>
  );
}
