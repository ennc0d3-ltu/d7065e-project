package detector

import (
	"errors"
	"math"
	"math/rand"
	"slices"
)

// Node is one isolation-tree node. A node without children is a leaf that
// holds the number of training points that reached it.
type Node struct {
	Feature int     `json:"f,omitempty"`
	Split   float64 `json:"s,omitempty"`
	Left    *Node   `json:"l,omitempty"`
	Right   *Node   `json:"r,omitempty"`
	Size    int     `json:"n,omitempty"`
}

// Forest is an isolation forest (Liu, Ting, Zhou 2008), the lightweight
// anomaly model chosen in proposal §4: trained on normal data only, so a
// real fire shows up as a joint deviation rather than a threshold crossing.
// Scores are in (0,1); higher means more anomalous.
type Forest struct {
	Dim   int     `json:"dim"`
	Psi   int     `json:"psi"` // sub-sample size used per tree
	Trees []*Node `json:"trees"`
}

// Train builds nTrees trees, each on a random sub-sample of up to sampleSize
// rows. A fixed seed makes training reproducible, which the evaluation needs.
func Train(data [][]float64, nTrees, sampleSize int, seed int64) (*Forest, error) {
	if len(data) == 0 {
		return nil, errors.New("detector: no training data")
	}
	if nTrees < 1 || sampleSize < 1 {
		return nil, errors.New("detector: trees and sample size must be positive")
	}
	dim := len(data[0])
	for _, row := range data {
		if len(row) != dim {
			return nil, errors.New("detector: training rows have different lengths")
		}
	}
	psi := min(sampleSize, len(data))
	limit := int(math.Ceil(math.Log2(float64(max(psi, 2)))))
	rng := rand.New(rand.NewSource(seed))

	f := &Forest{Dim: dim, Psi: psi}
	for range nTrees {
		perm := rng.Perm(len(data))[:psi]
		sub := make([][]float64, psi)
		for i, idx := range perm {
			sub[i] = data[idx]
		}
		f.Trees = append(f.Trees, build(sub, 0, limit, rng))
	}
	return f, nil
}

func build(data [][]float64, depth, limit int, rng *rand.Rand) *Node {
	if depth >= limit || len(data) <= 1 {
		return &Node{Size: len(data)}
	}
	dim := len(data[0])
	// pick a random feature among those that actually vary
	order := rng.Perm(dim)
	for _, feat := range order {
		lo, hi := data[0][feat], data[0][feat]
		for _, row := range data {
			lo, hi = math.Min(lo, row[feat]), math.Max(hi, row[feat])
		}
		if hi <= lo {
			continue
		}
		split := lo + rng.Float64()*(hi-lo)
		var left, right [][]float64
		for _, row := range data {
			if row[feat] < split {
				left = append(left, row)
			} else {
				right = append(right, row)
			}
		}
		if len(left) == 0 || len(right) == 0 {
			continue
		}
		return &Node{
			Feature: feat, Split: split,
			Left:  build(left, depth+1, limit, rng),
			Right: build(right, depth+1, limit, rng),
		}
	}
	return &Node{Size: len(data)} // every feature constant: cannot split
}

// Baseline is the per-feature median of the training data, ignoring missing
// (NaN) values. A classifier substitutes it for a failed sensor's features so
// the forest can still score the signals that remain.
func Baseline(train [][]float64) []float64 {
	if len(train) == 0 {
		return nil
	}
	out := make([]float64, len(train[0]))
	for j := range out {
		var col []float64
		for _, row := range train {
			if !math.IsNaN(row[j]) {
				col = append(col, row[j])
			}
		}
		if len(col) == 0 {
			continue
		}
		slices.Sort(col)
		out[j] = col[len(col)/2]
		if len(col)%2 == 0 {
			out[j] = (col[len(col)/2-1] + col[len(col)/2]) / 2
		}
	}
	return out
}

// Score returns the anomaly score of x in (0,1).
func (f *Forest) Score(x []float64) (float64, error) {
	if len(x) != f.Dim {
		return 0, errors.New("detector: feature vector has the wrong length")
	}
	if len(f.Trees) == 0 {
		return 0, errors.New("detector: forest has no trees")
	}
	var total float64
	for _, tree := range f.Trees {
		total += pathLength(tree, x, 0)
	}
	mean := total / float64(len(f.Trees))
	c := avgPath(f.Psi)
	if c == 0 {
		return 0.5, nil
	}
	return math.Pow(2, -mean/c), nil
}

func pathLength(n *Node, x []float64, depth float64) float64 {
	for n.Left != nil {
		if x[n.Feature] < n.Split {
			n = n.Left
		} else {
			n = n.Right
		}
		depth++
	}
	return depth + avgPath(n.Size)
}

// avgPath is c(n), the average path length of an unsuccessful BST search,
// used to normalise depths and to account for unsplit leaves.
func avgPath(n int) float64 {
	if n <= 1 {
		return 0
	}
	if n == 2 {
		return 1
	}
	h := math.Log(float64(n-1)) + 0.5772156649
	return 2*h - 2*float64(n-1)/float64(n)
}
