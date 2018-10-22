// Copyright (C) 2018 Storj Labs, Inc.
// See LICENSE for copying information.

package audit

import (
	"context"
	"time"

	"go.uber.org/zap"

	"storj.io/storj/pkg/overlay"
	"storj.io/storj/pkg/pointerdb/pdbclient"
	"storj.io/storj/pkg/provider"
	"storj.io/storj/pkg/transport"
)

// Service helps coordinate Cursor and Verifier to run the audit process continuously
type Service struct {
	Cursor   *Cursor
	Verifier *Verifier
	Reporter reporter
}

// Config contains configurable values for audit service
type Config struct {
	APIKey           string        `help:"APIKey to access the statdb"`
	StatDBAddr       string        `help:"address to contact statDB client"`
	MaxRetriesStatDB int           `help:"max number of times to attempt updating a statdb batch" default:"3"`
	PointerDBAddr    string        `help:"address to contact the pointerDB client"`
	TransportAddr    string        `help:"address to contact the transport client"`
	OverlayAddr      string        `help:"address to contact the overlay client"`
	Interval         time.Duration `help:"how frequently segments are audited" default:"30s"`
}

// Run runs the repairer with the configured values
func (c Config) Run(ctx context.Context, server *provider.Provider) (err error) {
	ca, err := provider.NewCA(ctx, 12, 4)
	if err != nil {
		return err
	}
	identity, err := ca.NewIdentity()
	if err != nil {
		return err
	}
	pointers, err := pdbclient.NewClient(identity, c.PointerDBAddr, c.APIKey)
	if err != nil {
		return err
	}
	overlay, err := overlay.NewOverlayClient(identity, c.OverlayAddr)
	if err != nil {
		return err
	}
	transport := transport.NewClient(identity)

	cursor := NewCursor(pointers)
	verifier := NewVerifier(transport, overlay, *identity)
	reporter, err := NewReporter(ctx, c.StatDBAddr, c.MaxRetriesStatDB)
	if err != nil {
		return err
	}

	service, err := NewService(cursor, verifier, reporter)
	if err != nil {
		return err
	}
	return service.Run(ctx, c.Interval)
}

// NewService instantiates a Service with access to a Cursor and Verifier
func NewService(cursor *Cursor, verifier *Verifier, reporter *Reporter) (service *Service, err error) {
	return &Service{
		Cursor:   cursor,
		Verifier: verifier,
		Reporter: reporter,
	}, nil
}

// Run calls Cursor and Verifier to continuously request random pointers, then verify data correctness at
// a random stripe within a segment
func (service *Service) Run(ctx context.Context, interval time.Duration) (err error) {
	defer mon.Task()(&ctx)(&err)

	zap.S().Info("Audit cron is starting up")
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	for {
		select {
		case <-ticker.C:
			stripe, err := service.Cursor.NextStripe(ctx)
			if err != nil {
				return err
			}

			verifiedNodes, err := service.Verifier.verify(ctx, stripe.Index, stripe.Segment, stripe.Authorization)
			if err != nil {
				return err
			}
			err = service.Reporter.RecordAudits(ctx, verifiedNodes)
			// TODO: if Error.Has(err) then log the error because it means not all node stats updated
			if err != nil {
				return err
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}
