// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/otfabric/sparkplugb-interop/harness"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	for _, b := range harness.DefaultBrokers() {
		fmt.Printf("waiting for %s on port %d... ", b.Name, b.Port)
		if err := harness.WaitForBroker(ctx, b); err != nil {
			fmt.Printf("FAIL: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("ok")
	}
}
