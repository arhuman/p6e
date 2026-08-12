package spike

import (
	"context"
	"fmt"
	"testing"
)

// chainLengths: 5 is the handoff's source/noop/noop/noop/sink shape; 100
// amortizes fixed per-run costs so the per-edge figure is readable.
var chainLengths = []int{5, 100}

var (
	sinkValue   Value
	sinkPayload Payload
	sinkAny     any
)

func benchInput() (Payload, Value) {
	p := Payload{N: 1, Data: make([]byte, 256)}
	return p, Value{Type: "Payload", ptr: p}
}

func BenchmarkBaselineTyped(b *testing.B) {
	for _, n := range chainLengths {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			ctx := context.Background()
			chain := BuildBaselineChain(n)
			in, _ := benchInput()
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				out, err := RunBaseline(ctx, chain, in)
				if err != nil {
					b.Fatal(err)
				}
				sinkPayload = out
			}
			reportPerEdge(b, n)
		})
	}
}

func BenchmarkErased(b *testing.B) {
	for _, n := range chainLengths {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			ctx := context.Background()
			chain := BuildErasedChain(n)
			_, in := benchInput()
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				out, err := RunErased(ctx, chain, in)
				if err != nil {
					b.Fatal(err)
				}
				sinkValue = out
			}
			reportPerEdge(b, n)
		})
	}
}

func BenchmarkErasedPtr(b *testing.B) {
	for _, n := range chainLengths {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			ctx := context.Background()
			chain := BuildErasedChainPtr(n)
			p := &Payload{N: 1, Data: make([]byte, 256)}
			in := Value{Type: "*Payload", ptr: p}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				out, err := RunErased(ctx, chain, in)
				if err != nil {
					b.Fatal(err)
				}
				sinkValue = out
			}
			reportPerEdge(b, n)
		})
	}
}

func BenchmarkReflect(b *testing.B) {
	for _, n := range chainLengths {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			ctx := context.Background()
			chain := BuildReflectChain(n)
			in, _ := benchInput()
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				out, err := RunReflect(ctx, chain, in)
				if err != nil {
					b.Fatal(err)
				}
				sinkAny = out
			}
			reportPerEdge(b, n)
		})
	}
}

func BenchmarkComposed(b *testing.B) {
	for _, n := range chainLengths {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			ctx := context.Background()
			fused := BuildComposedChain(n)
			_, in := benchInput()
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				r := fused(ctx, in)
				if r.Err != nil {
					b.Fatal(r.Err)
				}
				sinkValue = r.Value
			}
			reportPerEdge(b, n)
		})
	}
}

// BenchmarkComposeBuild measures what composition costs at plan build time,
// which is the price paid for its cheaper hot path.
func BenchmarkComposeBuild(b *testing.B) {
	for _, n := range chainLengths {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				BuildComposedChain(n)
			}
		})
	}
}

// reportPerEdge turns a whole-chain figure into the number the architecture
// decision actually turns on: engine cost per edge.
func reportPerEdge(b *testing.B, edges int) {
	b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N)/float64(edges), "ns/edge")
}
