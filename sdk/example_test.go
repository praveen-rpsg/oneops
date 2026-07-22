package sdk_test

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/rpsg/oneops/sdk"
)

// ExampleNewClient shows minimal client construction.
func ExampleNewClient() {
	client, err := sdk.NewClient(sdk.Config{
		BaseURL: "https://oneops.internal:8080",
		Token:   "my-bearer-token",
	})
	if err != nil {
		log.Fatal(err)
	}
	_ = client
}

// ExampleGovernanceClient_Ratify shows a write with idempotency + concurrency.
func ExampleGovernanceClient_Ratify() {
	client, _ := sdk.NewClient(sdk.Config{BaseURL: "https://oneops.internal:8080", Token: "tok"})

	res, err := client.Governance.Ratify(context.Background(), "ONEOPS-CFG-0007", sdk.WriteOptions{
		OperationID:        "req-2026-07-22-abc", // idempotency key
		ExpectedRowVersion: 3,                    // optimistic concurrency
	})
	if err != nil {
		switch {
		case sdk.IsConflict(err):
			log.Println("state or version conflict")
		case sdk.IsForbidden(err):
			log.Println("not permitted")
		default:
			log.Println(err)
		}
		return
	}
	fmt.Printf("now %s (v%d), audit recorded=%v\n", res.State.Lifecycle, res.RowVersion, res.Audit.Recorded)
}

// ExampleQueryClient_History shows cursor pagination over history.
func ExampleQueryClient_History() {
	client, _ := sdk.NewClient(sdk.Config{BaseURL: "https://oneops.internal:8080", Token: "tok"})
	ctx := context.Background()

	cursor := ""
	for {
		page, err := client.Query.History(ctx, "ONEOPS-CFG-0007", sdk.PageOptions{Limit: 100, Cursor: cursor})
		if err != nil {
			log.Fatal(err)
		}
		for _, item := range page.Items {
			fmt.Printf("#%d %s by %s\n", item.Seq, item.Operation, item.Actor)
		}
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
}

// ExampleAdminClient_RunIntegrity shows an on-demand integrity sweep with hooks.
func ExampleAdminClient_RunIntegrity() {
	client, _ := sdk.NewClient(sdk.Config{
		BaseURL:    "https://oneops.internal:8080",
		Token:      "admin-token",
		Timeout:    10 * time.Second,
		MaxRetries: 3,
		Hooks: sdk.Hooks{
			OnRetry: func(_ context.Context, method, path string, attempt int, err error) {
				log.Printf("retry %d %s %s: %v", attempt, method, path, err)
			},
		},
	})

	run, err := client.Admin.RunIntegrity(context.Background())
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("verified %d/%d chains, healthy=%v\n", run.ChainsOK, run.ChainsTotal, run.Healthy)
}
