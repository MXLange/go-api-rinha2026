package knn

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

type Reference struct {
	Vector QueryVector
	Label  uint8
}

type jsonReference struct {
	Vector []float64 `json:"vector"`
	Label  string    `json:"label"`
}

type buildNode struct {
	left  int32
	right int32
	start int
	len   int
	min   QueryVector
	max   QueryVector
}

const (
	maxInt16 = int16(32767)
	minInt16 = int16(-32768)
)

func BuildFile(inputPath string, outputPath string, leafSize int) error {
	refs, err := LoadReferences(inputPath)
	if err != nil {
		return err
	}
	data, err := Build(refs, leafSize)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(outputPath), 0o755); err != nil {
		return err
	}
	return os.WriteFile(outputPath, data, 0o644)
}

func LoadReferences(path string) ([]Reference, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	gz, err := gzip.NewReader(file)
	if err != nil {
		return nil, err
	}
	defer gz.Close()

	dec := json.NewDecoder(gz)
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	delim, ok := tok.(json.Delim)
	if !ok || delim != '[' {
		return nil, fmt.Errorf("expected top-level array")
	}

	refs := make([]Reference, 0, 3_100_000)
	for dec.More() {
		var item jsonReference
		if err := dec.Decode(&item); err != nil {
			return nil, err
		}
		if len(item.Vector) != Dims {
			return nil, fmt.Errorf("expected %d dims, got %d", Dims, len(item.Vector))
		}
		label := uint8(0)
		if item.Label == "fraud" {
			label = 1
		}
		refs = append(refs, Reference{
			Vector: QuantizeReference(item.Vector),
			Label:  label,
		})
	}
	return refs, nil
}

func Build(refs []Reference, leafSize int) ([]byte, error) {
	if len(refs) == 0 {
		return nil, fmt.Errorf("empty reference set")
	}
	if leafSize < 32 {
		leafSize = 32
	}
	if leafSize > 2048 {
		leafSize = 2048
	}

	partitions := make(map[uint32][]int, 128)
	for i := range refs {
		key := PartitionKey(&refs[i].Vector)
		partitions[key] = append(partitions[key], i)
	}

	keys := make([]uint32, 0, len(partitions))
	for key := range partitions {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })

	nodes := make([]buildNode, 0, len(refs)/leafSize*2)
	blocks := make([]Reference, 0, len(refs)+Lanes)
	partitionRoots := make([]struct {
		key  uint32
		root int
	}, 0, len(keys))

	for _, key := range keys {
		root := buildTree(refs, partitions[key], leafSize, &blocks, &nodes)
		partitionRoots = append(partitionRoots, struct {
			key  uint32
			root int
		}{key: key, root: root})
	}

	if len(blocks)%Lanes != 0 {
		return nil, fmt.Errorf("internal error: block padding mismatch")
	}

	var out bytes.Buffer
	blockCount := len(blocks) / Lanes
	writeHeader(&out, len(refs), len(partitionRoots), len(nodes), blockCount)

	for _, part := range partitionRoots {
		root := nodes[part.root]
		writeU32(&out, part.key)
		writeI32(&out, int32(part.root))
		writeI32(&out, int32(root.len))
		writeVector(&out, &root.min)
		writeVector(&out, &root.max)
	}

	for i := range nodes {
		n := nodes[i]
		writeI32(&out, n.left)
		writeI32(&out, n.right)
		writeI32(&out, int32(n.start/Lanes))
		writeI32(&out, int32(n.len))
		writeVector(&out, &n.min)
		writeVector(&out, &n.max)
	}

	for b := 0; b < blockCount; b++ {
		for d := 0; d < Dims; d++ {
			for lane := 0; lane < Lanes; lane++ {
				writeI16(&out, blocks[b*Lanes+lane].Vector[d])
			}
		}
	}
	for b := 0; b < blockCount; b++ {
		for lane := 0; lane < Lanes; lane++ {
			out.WriteByte(blocks[b*Lanes+lane].Label)
		}
	}

	return out.Bytes(), nil
}

func buildTree(refs []Reference, indices []int, leafSize int, blocks *[]Reference, nodes *[]buildNode) int {
	min, max := bounds(refs, indices)
	nodeIdx := len(*nodes)
	*nodes = append(*nodes, buildNode{left: -1, right: -1, min: min, max: max})

	if len(indices) <= leafSize {
		start := len(*blocks)
		blockCount := (len(indices) + Lanes - 1) / Lanes
		for b := 0; b < blockCount; b++ {
			for lane := 0; lane < Lanes; lane++ {
				i := b*Lanes + lane
				if i < len(indices) {
					*blocks = append(*blocks, refs[indices[i]])
				} else {
					*blocks = append(*blocks, Reference{})
				}
			}
		}
		(*nodes)[nodeIdx] = buildNode{
			left:  -1,
			right: -1,
			start: start,
			len:   len(indices),
			min:   min,
			max:   max,
		}
		return nodeIdx
	}

	splitDim := widestDimension(&min, &max)
	sorted := append([]int(nil), indices...)
	sort.Slice(sorted, func(i, j int) bool {
		return refs[sorted[i]].Vector[splitDim] < refs[sorted[j]].Vector[splitDim]
	})

	leftLen := len(sorted) / 2
	left := buildTree(refs, sorted[:leftLen], leafSize, blocks, nodes)
	right := buildTree(refs, sorted[leftLen:], leafSize, blocks, nodes)
	leftNode := (*nodes)[left]
	rightNode := (*nodes)[right]

	(*nodes)[nodeIdx] = buildNode{
		left:  int32(left),
		right: int32(right),
		start: leftNode.start,
		len:   leftNode.len + rightNode.len,
		min:   min,
		max:   max,
	}
	return nodeIdx
}

func bounds(refs []Reference, indices []int) (QueryVector, QueryVector) {
	var min QueryVector
	var max QueryVector
	for i := 0; i < PackedDims; i++ {
		min[i] = maxInt16
		max[i] = minInt16
	}
	for _, idx := range indices {
		v := refs[idx].Vector
		for d := 0; d < PackedDims; d++ {
			if v[d] < min[d] {
				min[d] = v[d]
			}
			if v[d] > max[d] {
				max[d] = v[d]
			}
		}
	}
	return min, max
}

func widestDimension(min *QueryVector, max *QueryVector) int {
	bestDim := 0
	bestWidth := int32(minInt16)
	for d := 0; d < Dims; d++ {
		width := int32(max[d]) - int32(min[d])
		if width > bestWidth {
			bestWidth = width
			bestDim = d
		}
	}
	return bestDim
}

func writeHeader(out *bytes.Buffer, refCount int, partitionCount int, nodeCount int, blockCount int) {
	header := make([]byte, headerSize)
	copy(header[:8], magic)
	binary.LittleEndian.PutUint32(header[8:], uint32(Scale))
	binary.LittleEndian.PutUint32(header[12:], Dims)
	binary.LittleEndian.PutUint32(header[16:], PackedDims)
	binary.LittleEndian.PutUint32(header[20:], Lanes)
	binary.LittleEndian.PutUint32(header[24:], uint32(refCount))
	binary.LittleEndian.PutUint32(header[28:], uint32(partitionCount))
	binary.LittleEndian.PutUint32(header[32:], uint32(nodeCount))
	binary.LittleEndian.PutUint32(header[36:], uint32(blockCount))
	out.Write(header)
}

func writeVector(out *bytes.Buffer, v *QueryVector) {
	for i := 0; i < PackedDims; i++ {
		writeI16(out, v[i])
	}
}

func writeI16(out *bytes.Buffer, value int16) {
	var buf [2]byte
	binary.LittleEndian.PutUint16(buf[:], uint16(value))
	out.Write(buf[:])
}

func writeI32(out *bytes.Buffer, value int32) {
	var buf [4]byte
	binary.LittleEndian.PutUint32(buf[:], uint32(value))
	out.Write(buf[:])
}

func writeU32(out *bytes.Buffer, value uint32) {
	var buf [4]byte
	binary.LittleEndian.PutUint32(buf[:], value)
	out.Write(buf[:])
}
