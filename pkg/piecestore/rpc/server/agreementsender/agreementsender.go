// Copyright (C) 2018 Storj Labs, Inc.
// See LICENSE for copying information.

package agreementsender

import (
	"flag"
	"log"
	"sync"
	"time"

	"github.com/zeebo/errs"
	"golang.org/x/net/context"
	"google.golang.org/grpc"

	"storj.io/storj/pkg/node"
	"storj.io/storj/pkg/overlay"
	"storj.io/storj/pkg/pb"
	"storj.io/storj/pkg/piecestore/rpc/server/psdb"
	"storj.io/storj/pkg/provider"
	"storj.io/storj/pkg/utils"
)

var (
	defaultCheckInterval = flag.Duration("piecestore.agreementsender.check_interval", time.Hour, "number of seconds to sleep between agreement checks")
	defaultOverlayAddr   = flag.String("piecestore.agreementsender.overlay_addr", "127.0.0.1:7777", "Overlay Address")

	// ASError wraps errors returned from agreementsender package
	ASError = errs.Class("agreement sender error")
)

// AgreementSender maintains variables required for reading bandwidth agreements from a DB and sending them to a Payers
type AgreementSender struct {
	DB       *psdb.DB
	overlay  overlay.Client
	identity *provider.FullIdentity
	errs     []error
	mu       sync.Mutex
}

// Initialize the Agreement Sender
func Initialize(DB *psdb.DB, identity *provider.FullIdentity) (*AgreementSender, error) {
	overlay, err := overlay.NewOverlayClient(identity, *defaultOverlayAddr)
	if err != nil {
		return nil, err
	}

	return &AgreementSender{DB: DB, identity: identity, overlay: overlay}, nil
}

// Run the afreement sender with a context to cehck for cancel
func (as *AgreementSender) Run(ctx context.Context) error {
	log.Println("AgreementSender is starting up")

	type agreementGroup struct {
		satellite  string
		agreements []*psdb.Agreement
	}

	c := make(chan *agreementGroup, 1)

	ticker := time.NewTicker(*defaultCheckInterval)
	defer ticker.Stop()
	go func() {
		for range ticker.C {
			agreementGroups, err := as.DB.GetBandwidthAllocations()
			if err != nil {
				as.appendErr(err)
				continue
			}

			// Send agreements in groups by satellite id to open less connections
			for satellite, agreements := range agreementGroups {
				c <- &agreementGroup{satellite, agreements}
			}
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return utils.CombineErrors(as.errs...)
		case agreementGroup := <-c:
			go func() {
				log.Printf("Sending Sending %v agreements to satellite %s\n", len(agreementGroup.agreements), agreementGroup.satellite)

				// Get satellite ip from overlay by Lookup agreementGroup.satellite
				satellite, err := as.overlay.Lookup(ctx, node.IDFromString(agreementGroup.satellite))
				if err != nil {
					as.appendErr(err)
					return
				}

				// Create client from satellite ip
				identOpt, err := as.identity.DialOption()
				if err != nil {
					as.appendErr(err)
					return
				}

				var conn *grpc.ClientConn
				conn, err = grpc.Dial(satellite.GetAddress().String(), identOpt)
				if err != nil {
					as.appendErr(err)
					return
				}

				client := pb.NewBandwidthClient(conn)
				stream, err := client.BandwidthAgreements(ctx)
				if err != nil {
					as.appendErr(err)
					return
				}

				defer func() {
					summary, closeErr := stream.CloseAndRecv(); 
					if closeErr != nil {
						log.Printf("error closing stream %s :: %v.Send() = %v", closeErr, stream, closeErr)
						return
					}

					// Delete from PSDB by signature
					for v := range summary.GetFailed() {
						if err = as.DB.DeleteBandwidthAllocationBySignature(agreementGroup.agreements[v].Signature); err != nil {
							log.Printf("error deleting bandwidth allocation index %v", v)
						}
					}
				}()

				for _, agreement := range agreementGroup.agreements {
					log.Println(agreement)

					msg := &pb.RenterBandwidthAllocation{
						Data:      agreement.Agreement,
						Signature: agreement.Signature,
					}

					// Send agreement to satellite
					if err = stream.Send(msg); err != nil {
						as.appendErr(err)
						return
					}
				}
			}()
		}
	}
}

func (as *AgreementSender) appendErr(err error) {
	// TODO: Should we cancel the context if a certain number of errors show up?
	as.mu.Lock()
	as.errs = append(as.errs, err)
	as.mu.Unlock()
}