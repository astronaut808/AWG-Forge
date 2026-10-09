package app

import (
	"context"
	"errors"

	"github.com/astronaut808/awg-forge/internal/controlapi"
	"github.com/astronaut808/awg-forge/internal/storage"
)

// NodeEnrollmentSession holds the same exclusive lease as serve. First join,
// like rebind, is explicitly offline; no live serve owner is bypassed.
type NodeEnrollmentSession struct {
	service *Service
	lease   *storage.StateLock
}

func (s *Service) BeginNodeEnrollment(ctx context.Context) (*NodeEnrollmentSession, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	lease, err := storage.AcquireStateLock(s.cfg.ConfigDir)
	if err != nil {
		return nil, errors.New("node enrollment requires a stopped server")
	}
	if err := s.PreflightNodeEnrollment(ctx); err != nil {
		_ = lease.Close()
		return nil, err
	}
	return &NodeEnrollmentSession{service: s, lease: lease}, nil
}

func (r *NodeEnrollmentSession) Close() error {
	if r.lease == nil {
		return nil
	}
	err := r.lease.Close()
	r.lease = nil
	return err
}
func (r *NodeEnrollmentSession) BootID() (string, error) { return r.service.BootID() }
func (r *NodeEnrollmentSession) PreflightNodeEnrollment(ctx context.Context) error {
	if r.lease == nil {
		return errors.New("node enrollment lease ended")
	}
	return r.service.PreflightNodeEnrollment(ctx)
}
func (r *NodeEnrollmentSession) InstallNodeEnrollment(ctx context.Context, invitation controlapi.Invitation, approved controlapi.EnrollmentStatus, key []byte) error {
	if r.lease == nil {
		return errors.New("node enrollment lease ended")
	}
	return r.service.InstallNodeEnrollment(ctx, invitation, approved, key)
}
