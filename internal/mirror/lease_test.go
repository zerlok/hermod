package mirror

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// leaseOp is one step of a lease scenario, by a named holder. Every holder is
// its own open file, which flock treats as its own process.
type leaseOp struct {
	do     string // "take", "release" or "close"
	holder string
	want   bool // take: the lease is granted; release: the holder is the last
}

func TestLease(t *testing.T) {
	cases := []struct {
		name string
		ops  []leaseOp
	}{
		{"a sole holder is the last", []leaseOp{
			{"take", "a", true}, {"release", "a", true},
		}},
		{"of two holders the first to leave is not the last, the second is", []leaseOp{
			{"take", "a", true}, {"take", "b", true},
			{"release", "a", false}, {"close", "a", false},
			{"release", "b", true},
		}},
		{"two holders leaving at once: the last to try is the last", []leaseOp{
			{"take", "a", true}, {"take", "b", true},
			{"release", "a", false}, {"release", "b", true},
		}},
		{"a holder that ends without releasing leaves no share behind", []leaseOp{
			{"take", "a", true}, {"take", "b", true},
			{"close", "a", false},
			{"release", "b", true},
		}},
		{"a new holder waits while the last one settles", []leaseOp{
			{"take", "a", true}, {"release", "a", true},
			{"take", "b", false},
			{"close", "a", false},
			{"take", "b", true}, {"release", "b", true},
		}},
		{"a holder that left keeps nobody from being last", []leaseOp{
			{"take", "a", true}, {"take", "b", true},
			{"release", "a", false}, {"close", "a", false},
			{"take", "c", true},
			{"release", "b", false}, {"release", "c", true},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "lease")
			held := map[string]*Lease{}
			t.Cleanup(func() {
				for _, l := range held {
					_ = l.Close()
				}
			})
			for i, op := range tc.ops {
				switch op.do {
				case "take":
					ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
					l, err := TakeLease(ctx, dir, "hermod-box-dev-api")
					cancel()
					if (err == nil) != op.want {
						t.Fatalf("step %d: take by %s: err = %v, want granted %v", i, op.holder, err, op.want)
					}
					if err == nil {
						held[op.holder] = l
					}
				case "release":
					last, err := held[op.holder].Release()
					if err != nil || last != op.want {
						t.Fatalf("step %d: release by %s = %v, %v, want last %v", i, op.holder, last, err, op.want)
					}
				case "close":
					if err := held[op.holder].Close(); err != nil {
						t.Fatalf("step %d: close by %s: %v", i, op.holder, err)
					}
					delete(held, op.holder)
				}
			}
		})
	}
}

func TestLeaseFile(t *testing.T) {
	cases := []struct {
		name     string
		dir      string // under a temporary directory
		wantMode os.FileMode
	}{
		{"made private, parents included", "run/hermod/lease", 0o700},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), tc.dir)
			l, err := TakeLease(context.Background(), dir, "hermod-box-dev-api")
			if err != nil {
				t.Fatalf("TakeLease() error: %v", err)
			}
			defer l.Close()
			info, err := os.Stat(dir)
			if err != nil {
				t.Fatal(err)
			}
			if got := info.Mode().Perm(); got != tc.wantMode {
				t.Errorf("lease directory mode = %v, want %v", got, tc.wantMode)
			}
			if _, err := os.Stat(filepath.Join(dir, "hermod-box-dev-api")); err != nil {
				t.Errorf("lease file: %v", err)
			}
		})
	}
}
