package knn

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

const (
	magic      = "GOKNN001"
	headerSize = 64
	partSize   = 76
	nodeSize   = 80

	madvHugePage = 14
	maxInt64     = int64(^uint64(0) >> 1)
)

type partition struct {
	key    uint32
	root   int32
	length int32
	min    QueryVector
	max    QueryVector
}

type node struct {
	left  int32
	right int32
	start int32
	len   int32
	min   QueryVector
	max   QueryVector
}

type Index struct {
	data       []byte
	partitions []partition
	nodes      []node
	vectors    []int16
	labels     []byte
}

func Open(path string) (*Index, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	stat, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if stat.Size() < headerSize {
		return nil, errors.New("index too small")
	}

	data, err := syscall.Mmap(int(file.Fd()), 0, int(stat.Size()), syscall.PROT_READ, syscall.MAP_SHARED)
	if err != nil {
		return nil, err
	}

	idx, err := openMapped(data)
	if err != nil {
		_ = syscall.Munmap(data)
		return nil, err
	}
	return idx, nil
}

func openMapped(data []byte) (*Index, error) {
	if string(data[:8]) != magic {
		return nil, errors.New("bad index magic")
	}
	scale := int32(le32(data[8:]))
	dims := int32(le32(data[12:]))
	packedDims := int32(le32(data[16:]))
	lanes := int32(le32(data[20:]))
	if scale != int32(Scale) || dims != Dims || packedDims != PackedDims || lanes != Lanes {
		return nil, fmt.Errorf("incompatible index scale=%d dims=%d packed=%d lanes=%d", scale, dims, packedDims, lanes)
	}

	partCount := int(le32(data[28:]))
	nodeCount := int(le32(data[32:]))
	blockCount := int(le32(data[36:]))
	if partCount < 0 || nodeCount < 0 || blockCount < 0 {
		return nil, errors.New("negative index counts")
	}

	partsOff := headerSize
	nodesOff := partsOff + partCount*partSize
	vectorsOff := nodesOff + nodeCount*nodeSize
	vectorBytes := blockCount * Dims * Lanes * 2
	labelsOff := vectorsOff + vectorBytes
	labelsEnd := labelsOff + blockCount*Lanes
	if labelsEnd > len(data) {
		return nil, errors.New("truncated index")
	}

	partitions := make([]partition, partCount)
	for i := range partitions {
		readPartition(data[partsOff+i*partSize:], &partitions[i])
	}
	nodes := make([]node, nodeCount)
	for i := range nodes {
		readNode(data[nodesOff+i*nodeSize:], &nodes[i])
	}

	vectors := i16View(data[vectorsOff : vectorsOff+vectorBytes])
	labels := data[labelsOff:labelsEnd]
	_ = syscall.Madvise(data[vectorsOff:labelsEnd], madvHugePage)

	return &Index{
		data:       data,
		partitions: partitions,
		nodes:      nodes,
		vectors:    vectors,
		labels:     labels,
	}, nil
}

func (idx *Index) Close() error {
	if idx.data == nil {
		return nil
	}
	data := idx.data
	idx.data = nil
	idx.vectors = nil
	idx.labels = nil
	idx.nodes = nil
	idx.partitions = nil
	return syscall.Munmap(data)
}

func (idx *Index) PredictFraudCount(query *QueryVector) uint8 {
	var bestDists [K]int64
	var bestLabels [K]uint8
	for i := range bestDists {
		bestDists[i] = maxInt64
	}

	queryKey := PartitionKey(query)
	match := -1
	for i := range idx.partitions {
		if idx.partitions[i].key == queryKey {
			match = i
			break
		}
	}

	if match >= 0 {
		idx.searchNode(int(idx.partitions[match].root), 0, query, &bestDists, &bestLabels)
	}

	var candidates [256]candidate
	count := 0
	for i := range idx.partitions {
		if i == match {
			continue
		}
		bound := lowerBound(query, &idx.partitions[i].min, &idx.partitions[i].max)
		if bound >= bestDists[K-1] {
			continue
		}
		if count < len(candidates) {
			candidates[count] = candidate{idx: i, bound: bound}
			count++
		}
	}
	sortCandidates(candidates[:count])

	for i := 0; i < count; i++ {
		if candidates[i].bound >= bestDists[K-1] {
			break
		}
		part := &idx.partitions[candidates[i].idx]
		idx.searchNode(int(part.root), candidates[i].bound, query, &bestDists, &bestLabels)
	}

	var fraud uint8
	for _, label := range bestLabels {
		fraud += label
	}
	return fraud
}

type candidate struct {
	idx   int
	bound int64
}

func sortCandidates(items []candidate) {
	for i := 1; i < len(items); i++ {
		item := items[i]
		j := i - 1
		for j >= 0 && items[j].bound > item.bound {
			items[j+1] = items[j]
			j--
		}
		items[j+1] = item
	}
}

func (idx *Index) searchNode(root int, rootBound int64, query *QueryVector, bestDists *[K]int64, bestLabels *[K]uint8) {
	if root < 0 || root >= len(idx.nodes) {
		return
	}

	var stackNode [128]int
	var stackBound [128]int64
	stackLen := 0
	current := root
	currentBound := rootBound

	for {
		if currentBound < bestDists[K-1] {
			n := &idx.nodes[current]
			if n.left < 0 {
				idx.scanLeaf(n, query, bestDists, bestLabels)
			} else {
				left := int(n.left)
				right := int(n.right)
				lb := lowerBound(query, &idx.nodes[left].min, &idx.nodes[left].max)
				rb := lowerBound(query, &idx.nodes[right].min, &idx.nodes[right].max)

				if lb <= rb {
					if rb < bestDists[K-1] && stackLen < len(stackNode) {
						stackNode[stackLen] = right
						stackBound[stackLen] = rb
						stackLen++
					}
					current = left
					currentBound = lb
					continue
				}

				if lb < bestDists[K-1] && stackLen < len(stackNode) {
					stackNode[stackLen] = left
					stackBound[stackLen] = lb
					stackLen++
				}
				current = right
				currentBound = rb
				continue
			}
		}

		if stackLen == 0 {
			break
		}
		stackLen--
		current = stackNode[stackLen]
		currentBound = stackBound[stackLen]
	}
}

func (idx *Index) scanLeaf(n *node, query *QueryVector, bestDists *[K]int64, bestLabels *[K]uint8) {
	startBlock := int(n.start)
	length := int(n.len)
	blocks := (length + Lanes - 1) / Lanes

	for b := 0; b < blocks; b++ {
		block := startBlock + b
		base := block * Dims * Lanes
		labelsBase := block * Lanes
		laneCount := length - b*Lanes
		if laneCount > Lanes {
			laneCount = Lanes
		}
		for lane := 0; lane < laneCount; lane++ {
			dist := distanceLane(idx.vectors, base, lane, query)
			insertBest(dist, idx.labels[labelsBase+lane], bestDists, bestLabels)
		}
	}
}

func distanceLane(vectors []int16, base int, lane int, query *QueryVector) int64 {
	var dist int64
	for d := 0; d < Dims; d++ {
		diff := int64(vectors[base+d*Lanes+lane]) - int64(query[d])
		dist += diff * diff
	}
	return dist
}

func insertBest(dist int64, label uint8, bestDists *[K]int64, bestLabels *[K]uint8) {
	if dist >= bestDists[K-1] {
		return
	}
	pos := K - 1
	for pos > 0 && dist < bestDists[pos-1] {
		bestDists[pos] = bestDists[pos-1]
		bestLabels[pos] = bestLabels[pos-1]
		pos--
	}
	bestDists[pos] = dist
	bestLabels[pos] = label
}

func lowerBound(query *QueryVector, min *QueryVector, max *QueryVector) int64 {
	var dist int64
	for d := 0; d < Dims; d++ {
		q := query[d]
		var diff int64
		if q < min[d] {
			diff = int64(min[d]) - int64(q)
		} else if q > max[d] {
			diff = int64(q) - int64(max[d])
		}
		dist += diff * diff
	}
	return dist
}

func readPartition(buf []byte, out *partition) {
	out.key = binary.LittleEndian.Uint32(buf[0:])
	out.root = int32(binary.LittleEndian.Uint32(buf[4:]))
	out.length = int32(binary.LittleEndian.Uint32(buf[8:]))
	readVector(buf[12:], &out.min)
	readVector(buf[44:], &out.max)
}

func readNode(buf []byte, out *node) {
	out.left = int32(binary.LittleEndian.Uint32(buf[0:]))
	out.right = int32(binary.LittleEndian.Uint32(buf[4:]))
	out.start = int32(binary.LittleEndian.Uint32(buf[8:]))
	out.len = int32(binary.LittleEndian.Uint32(buf[12:]))
	readVector(buf[16:], &out.min)
	readVector(buf[48:], &out.max)
}

func readVector(buf []byte, out *QueryVector) {
	for i := 0; i < PackedDims; i++ {
		out[i] = int16(binary.LittleEndian.Uint16(buf[i*2:]))
	}
}

func le32(buf []byte) uint32 {
	return binary.LittleEndian.Uint32(buf)
}

func i16View(buf []byte) []int16 {
	if len(buf) == 0 {
		return nil
	}
	return unsafe.Slice((*int16)(unsafe.Pointer(&buf[0])), len(buf)/2)
}
