// Copyright (C) 2018 Storj Labs, Inc.
// See LICENSE for copying information.

package redis

import (
	"testing"

	"storj.io/storj/storage/redis/redisserver"
	"storj.io/storj/storage/testqueue"
)

func TestQueue(t *testing.T) {
	addr, cleanup, err := redisserver.Start()
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	client, err := NewClient(addr, "", 1)
	if err != nil {
		t.Fatal(err)
	}

	testqueue.RunTests(t, client)
}