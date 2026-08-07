package ledger

import "context"

type claimHolderContextKey struct{}

// ContextWithClaimHolder binds one controller's immutable claim holder to its writes.
func ContextWithClaimHolder(ctx context.Context, holder string) context.Context {
	return context.WithValue(ctx, claimHolderContextKey{}, holder)
}

func claimHolderFromContext(ctx context.Context) (string, bool) {
	holder, ok := ctx.Value(claimHolderContextKey{}).(string)
	return holder, ok && holder != ""
}
