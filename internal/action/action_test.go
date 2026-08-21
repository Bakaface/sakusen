package action_test

import (
	"github.com/Bakaface/sakusen/internal/action"
	"github.com/Bakaface/sakusen/internal/client"
)

// Compile-time check: *client.Client satisfies action.ClientAPI. Adding a
// method to ClientAPI without also adding it to *client.Client will fail to
// build here, so the contract stays in sync.
var _ action.ClientAPI = (*client.Client)(nil)
