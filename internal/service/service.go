package service

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"sync/atomic"
	"time"

	"mobile-use/internal/relay"
	"mobile-use/internal/store"
	"mobile-use/internal/wire"
)

type Service struct {
	Store     *store.Store
	Hub       *relay.Hub
	Video     *VideoService
	DigestKey []byte
	Admitted  atomic.Uint64
	Succeeded atomic.Uint64
	Failed    atomic.Uint64
	Unknown   atomic.Uint64
}

func (s *Service) Operation(ctx context.Context, actor wire.Actor, session string, request wire.Operation) (wire.Result, error) {
	if request.ID == "" {
		request.ID = wire.NewID()
	}
	digest := hmac.New(sha256.New, s.DigestKey)
	_, _ = digest.Write([]byte(wire.Marshal(request)))
	ticket, err := s.Store.BeginOperation(ctx, actor, session, request, hex.EncodeToString(digest.Sum(nil)), time.Now())
	if err != nil {
		return wire.Result{}, err
	}
	if ticket.Previous != nil {
		return *ticket.Previous, nil
	}
	s.Admitted.Add(1)
	command, cancel := context.WithDeadline(ctx, ticket.Dispatch.Deadline)
	defer cancel()
	result := wire.Result{ID: request.ID, State: wire.Failed}
	if err := s.Store.Confirm(command, ticket.Target, time.Now()); err != nil {
		result.Fault = wire.Public(err)
	} else {
		response, err := s.Hub.Dispatch(command, ticket.Target, ticket.Dispatch)
		if err != nil {
			result.Fault = wire.Public(err)
			if errors.Is(err, wire.ErrUnknown) {
				result.State = wire.Unknown
			}
		} else {
			result = response
			if result.ID != request.ID || result.State != wire.Succeeded && result.State != wire.Failed && result.State != wire.Unknown {
				result = wire.Result{ID: request.ID, State: wire.Unknown, Fault: wire.ErrUnknown}
			}
		}
	}
	finishCtx, finishCancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer finishCancel()
	if err := s.Store.FinishOperation(finishCtx, actor, ticket.Target, result, time.Now()); err != nil {
		slog.Error("Operation journal update failed", "operation", request.ID)
		return wire.Result{ID: request.ID, State: wire.Unknown, Fault: wire.ErrUnknown}, nil
	}
	switch result.State {
	case wire.Succeeded:
		s.Succeeded.Add(1)
	case wire.Unknown:
		s.Unknown.Add(1)
	default:
		s.Failed.Add(1)
	}
	if result.Observation != nil || result.Page != nil || result.Apps != nil || result.Contexts != nil || result.Sensor != nil || result.Artifact != nil {
		if err := s.Store.Confirm(finishCtx, ticket.Target, time.Now()); err != nil {
			return wire.Result{}, wire.ErrExpired
		}
	}
	return result, nil
}

func (s *Service) Change(ctx context.Context, actor wire.Actor, id, verb string) (wire.Session, error) {
	session, target, err := s.Store.ChangeSession(ctx, actor, id, verb, time.Now())
	if err == nil && target.Generation > 0 && (verb == "end" || target.Generation != session.Generation) {
		s.stop(ctx, target)
	}
	return session, err
}

func (s *Service) stop(ctx context.Context, target store.Target) {
	if s.Video != nil {
		s.Video.Stop(target)
	}
	stop, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
	defer cancel()
	s.Hub.Stop(stop, target)
}

func (s *Service) Maintain(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	prune := time.NewTicker(time.Hour)
	defer prune.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			check, cancel := context.WithTimeout(ctx, time.Second)
			targets, err := s.Store.Sweep(check, now)
			cancel()
			if err != nil && ctx.Err() == nil {
				slog.Warn("Session expiry sweep failed", "error", err)
			}
			for _, target := range targets {
				s.stop(ctx, target)
			}
		case now := <-prune.C:
			check, cancel := context.WithTimeout(ctx, 10*time.Second)
			if err := s.Store.Prune(check, now); err != nil {
				slog.Warn("Retention sweep failed", "error", err)
			}
			cancel()
		}
	}
}
