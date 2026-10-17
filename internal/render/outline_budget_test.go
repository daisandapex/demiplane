// SPDX-FileCopyrightText: 2026 Dais & Apex
// SPDX-License-Identifier: AGPL-3.0-only

package render

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// outlineChildEnv makes the test binary run the outline payload as a child.
const outlineChildEnv = "DEMIPLANE_OUTLINE_BUDGET_CHILD"

// outlineBudgetEnv carries the child's output budget in bytes.
const outlineBudgetEnv = "DEMIPLANE_OUTLINE_BUDGET_BYTES"

// outlinePayload is one very long h2 followed by many h3s. Every h3's outline
// entry used to repeat the h2's id, so a small input made an outline of a
// gigabyte or more before the final size check.
func outlinePayload() []byte {
	return []byte("## " + strings.Repeat("a", 65536) + "\n" + strings.Repeat("### b\n", 20000))
}

type outlineChildResult struct {
	Budget     bool   `json:"budget"`
	Err        string `json:"err"`
	OutLen     int    `json:"out_len"`
	TotalAlloc uint64 `json:"total_alloc"`
	Sys        uint64 `json:"sys"`
}

// TestOutlineBudgetChild is the child half of TestOutlineHonoursRenderBudget;
// it does nothing unless the parent started it.
func TestOutlineBudgetChild(t *testing.T) {
	if os.Getenv(outlineChildEnv) != "1" {
		t.Skip("child process only")
	}
	budget, _ := strconv.Atoi(os.Getenv(outlineBudgetEnv))
	var before runtime.MemStats
	runtime.ReadMemStats(&before)
	out, err := Render(context.Background(), outlinePayload(), Options{
		Header: true,
		Limits: Limits{MaxOutputBytes: budget},
	})
	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	res := outlineChildResult{
		Budget:     errors.Is(err, ErrBudget),
		OutLen:     len(out),
		TotalAlloc: after.TotalAlloc - before.TotalAlloc,
		Sys:        after.Sys,
	}
	if err != nil {
		res.Err = err.Error()
	}
	_ = json.NewEncoder(os.Stdout).Encode(res)
}

// TestOutlineHonoursRenderBudget renders the outline payload in a child
// process with a memory limit and checks that the render either fits its
// budget or is refused for it, without allocating far past it. The 1 MiB
// budget is smaller than the page, so that run must be refused.
func TestOutlineHonoursRenderBudget(t *testing.T) {
	if os.Getenv(outlineChildEnv) == "1" {
		t.Skip("parent only")
	}
	for _, tc := range []struct {
		budget     int
		wantRefuse bool
	}{
		{4 << 20, false},
		{1 << 20, true},
	} {
		t.Run(strconv.Itoa(tc.budget>>20)+"MiB", func(t *testing.T) {
			res := runOutlineChild(t, tc.budget)
			t.Logf("child: budget=%v err=%q out=%d total_alloc=%d MiB sys=%d MiB",
				res.Budget, res.Err, res.OutLen, res.TotalAlloc>>20, res.Sys>>20)
			// Memory the child took from the OS stays a small multiple of the
			// budget either way. Sys, not TotalAlloc: the race detector
			// multiplies allocation counts but not the peak a render holds
			// (the unbounded outline held gigabytes live).
			if maxSys := uint64(64<<20 + 8*tc.budget); res.Sys > maxSys {
				t.Errorf("render took %d MiB from the OS, want at most %d MiB (budget %d MiB)",
					res.Sys>>20, maxSys>>20, tc.budget>>20)
			}
			if tc.wantRefuse && !res.Budget {
				t.Errorf("a page over the budget was not refused (out %d, err %q)", res.OutLen, res.Err)
			}
			if !res.Budget && (res.Err != "" || res.OutLen > tc.budget) {
				t.Errorf("render neither fit the budget nor was refused for it: out %d, err %q", res.OutLen, res.Err)
			}
		})
	}
}

func runOutlineChild(t *testing.T, budget int) outlineChildResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestOutlineBudgetChild$", "-test.count=1")
	// GOMEMLIMIT makes the child collect hard instead of growing; the
	// allocation total is what the assertions read.
	cmd.Env = append(os.Environ(), outlineChildEnv+"=1", outlineBudgetEnv+"="+strconv.Itoa(budget), "GOMEMLIMIT=512MiB")
	out, err := cmd.Output()
	if ctx.Err() != nil {
		t.Fatalf("child render did not finish in 2m")
	}
	line := ""
	for _, l := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(l, "{") {
			line = l
		}
	}
	if line == "" {
		t.Fatalf("child gave no result (err %v): %s", err, out)
	}
	var res outlineChildResult
	if jerr := json.Unmarshal([]byte(line), &res); jerr != nil {
		t.Fatal(jerr)
	}
	return res
}
