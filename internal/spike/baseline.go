package spike

import "context"

// Baseline: a chain of typed function values, no erasure at all. This is the
// floor. It is not a viable engine (every node in the pipeline would have to
// share one input and output type), but it bounds what any bridge can cost.

// BuildBaselineChain builds a chain of n identity-ish typed functions.
func BuildBaselineChain(n int) []func(context.Context, Payload) Result[Payload] {
	chain := make([]func(context.Context, Payload) Result[Payload], n)
	for i := range chain {
		chain[i] = bump
	}
	return chain
}

// RunBaseline walks the typed chain.
func RunBaseline(ctx context.Context, chain []func(context.Context, Payload) Result[Payload], in Payload) (Payload, error) {
	cur := in
	for _, fn := range chain {
		r := fn(ctx, cur)
		if r.Err != nil {
			return Payload{}, r.Err
		}
		cur = r.Value
	}
	return cur, nil
}
