package entangler

import (
	"context"
	"ipfs-alpha-entanglement-code/util"
	"sync"

	"golang.org/x/xerrors"
)

type BlockGetter interface {
	GetData(index int) ([]byte, error)
	GetParity(index int, strand int) ([]byte, error)
}

type Lattice struct {
	*sync.Mutex

	Entangler
	DataBlocks   []*Block
	ParityBlocks [][]*Block

	Getter BlockGetter
	Once   sync.Once

	requestCounter uint

	SwitchDepth uint
}

// NewLattice creates a new lattice for block downloading and recovering
func NewLattice(alpha int, s int, p int, blockNum int, blockGetter BlockGetter, switchDepth uint) (lattice *Lattice) {
	var tangler = *NewEntangler(alpha, s, p)
	tangler.ChunkNum = blockNum
	lattice = &Lattice{
		Mutex:        &sync.Mutex{},
		Entangler:    tangler,
		DataBlocks:   make([]*Block, 0),
		ParityBlocks: make([][]*Block, alpha),
		Getter:       blockGetter,
		SwitchDepth:  switchDepth,
	}

	return lattice
}

// Init inits the lattice by creating the entire structure in memory
func (l *Lattice) Init() {
	l.Once.Do(func() {
		l.initDataBlocks()
		l.initParityBlocks()
		l.initLinks()
		util.LogPrintf("Finish initializing lattice")
	})
}

// GetAllData returns all data in the data blocks as a byte array
func (l *Lattice) GetAllData() (data [][]byte, err error) {
	for i := 0; i < l.ChunkNum; i++ {
		var chunk []byte
		chunk, _, err = l.GetChunk(i + 1)
		if err != nil {
			return data, err
		}
		data = append(data, chunk)
	}

	return data, nil
}

func (l *Lattice) UpdateParity(index int, strand int, data []byte) {
	l.ParityBlocks[strand][index].SetData(data, true)
}

// GetChunk returns a data chunk in the indexed block
func (l *Lattice) GetChunk(index int) (data []byte, repaired bool, err error) {
	block := l.getBlock(index)
	data, err = l.getDataFromBlock(block, l.SwitchDepth)
	repaired = block.IsRepaired()

	return data, repaired, err
}

// getBlock returns an original data block with the given index
func (l *Lattice) getBlock(index int) (block *Block) {
	block = l.DataBlocks[index-1]
	return block
}

// getDataFromBlock recovers a block with missing chunk using the lattice (hybrid, auto switch)
func (l *Lattice) getDataFromBlock(block *Block, allowDepth uint) ([]byte, error) {
	rid := l.getRequestID()
	if allowDepth > 0 {
		data, err := l.getDataFromBlockSequential(block, rid, allowDepth)
		if err == nil {
			return data, nil
		}
	}

	return nil, xerrors.Errorf("fail to recover block %d (parity: %t. strand: %d): %s.",
		block.Index, block.IsParity, block.Strand, "sequential recovery failed")

	// ctx, cancel := context.WithCancel(context.Background())
	// defer cancel()

	// return l.getDataFromBlockParallel(ctx, block, rid)
}

// getDataFromBlockSequential recovers a block with missing chunk using the lattice (single thread)
func (l *Lattice) getDataFromBlockSequential(block *Block, rid uint, allowDepth uint) (data []byte, err error) {
	l.sequentialRecoverHelper(block, rid, allowDepth)

	data, err = block.GetData()
	if err != nil {
		err = xerrors.Errorf("fail to recover block %d (parity: %t. strand: %d): %s.",
			block.Index, block.IsParity, block.Strand, err)
	}

	return data, err
}

// getDataFromBlockParallel recovers a block with missing chunk using the lattice (multiple threads)
func (l *Lattice) getDataFromBlockParallel(ctx context.Context, block *Block, rid uint) (data []byte, err error) {
	myChannel := make(chan bool, 1)
	l.parallelRecoverHelper(ctx, block, rid, myChannel)
	<-myChannel
	data, err = block.GetData()
	if err != nil {
		err = xerrors.Errorf("fail to recover block %d (parity: %t. strand: %d): %s.",
			block.Index, block.IsParity, block.Strand, err)
	}

	return data, err
}

// initDataBlocks inits data blocks when init lattice
func (l *Lattice) initDataBlocks() {
	// Create datablocks
	for i := 0; i < l.ChunkNum; i++ {
		datab := NewBlock(i+1, false)
		datab.LeftNeighbors = make([]*Block, l.Alpha)
		datab.RightNeighbors = make([]*Block, l.Alpha)
		l.DataBlocks = append(l.DataBlocks, datab)
	}
}

// initParityBlocks inits parity blocks when init lattice
func (l *Lattice) initParityBlocks() {
	// Create parities
	for k := 0; k < l.Alpha; k++ {
		for i := 0; i < l.ChunkNum; i++ {
			parityb := NewBlock(i+1, true)
			parityb.LeftNeighbors = make([]*Block, 1)
			parityb.RightNeighbors = make([]*Block, 1)
			parityb.Strand = k
			l.ParityBlocks[k] = append(l.ParityBlocks[k], parityb)
		}
	}
}

// initLinks inits links between data and parity blocks
func (l *Lattice) initLinks() {
	// Link
	for i := 0; i < l.ChunkNum; i++ {
		datab := l.DataBlocks[i]
		forward := l.getForwardNeighborIndexes(i + 1)
		for k := 0; k < l.Alpha; k++ {
			rightParity := l.ParityBlocks[k][i]
			rightParity.LeftNeighbors[0] = datab
			datab.RightNeighbors[k] = rightParity

			var rightDataBlock *Block
			if l.IsValidIndex(forward[k]) {
				rightDataBlock = l.DataBlocks[forward[k]-1]
			} else {
				// Wrap lattice
				index := l.getChainStartIndexes(i + 1)[k]
				rightDataBlock = l.DataBlocks[index-1]
				if rightDataBlock != datab {
					rightDataBlock.RightNeighbors[k].IsWrapModified = true
				}
			}
			rightParity.RightNeighbors[0] = rightDataBlock
			rightDataBlock.LeftNeighbors[k] = rightParity
		}
	}
}

// downloadBlock downloads data/parity blocks using the Getter passed in
func (l *Lattice) downloadBlock(block *Block) (err error) {
	var data []byte
	if block.IsParity {
		data, err = l.Getter.GetParity(block.Index, block.Strand)
	} else {
		data, err = l.Getter.GetData(block.Index)
	}
	if err == nil {
		block.SetData(data, false)
	}

	return err
}

// generate uniq id for the request
func (l *Lattice) getRequestID() uint {
	l.Lock()
	defer l.Unlock()

	id := l.requestCounter
	l.requestCounter++
	return id
}

// sequentialRepair repairs a block using single thread
func (l *Lattice) sequentialRepair(block *Block, rid uint, allowDepth uint) bool {
	if allowDepth == 0 {
		return false
	}
	pairs := block.GetRecoverPairs()
	if len(pairs) == 0 {
		return false
	}

	for _, mypair := range pairs {
		util.LogPrintf(util.Yellow("{Sequential} Left - Index: %d, Parity: %t, Strand: %d\n"+
			"Right - Index: %d, Parity: %t, Strand: %d\n\n"),
			mypair.Left.Index, mypair.Left.IsParity, mypair.Left.Strand,
			mypair.Right.Index, mypair.Right.IsParity, mypair.Right.Strand)

		leftChunk, RepairErr := l.getDataFromBlockSequential(mypair.Left, rid, allowDepth-1)
		if RepairErr != nil {
			continue
		}

		rightChunk, RepairErr := l.getDataFromBlockSequential(mypair.Right, rid, allowDepth-1)
		if RepairErr != nil {
			continue
		}

		if block.Recover(leftChunk, rightChunk) == nil {
			return true
		}
	}
	return false
}

// sequentialRecoverHelper is a helper function to recursively do the sequential recovery
func (l *Lattice) sequentialRecoverHelper(block *Block, rid uint, allowDepth uint) {
	var repairSuccess = false
	var modifyState = true
	defer func() {
		if modifyState {
			block.FinishRepair(repairSuccess)
		}
	}()

	// if already has data or already visited
	if !block.StartRepair(context.Background(), rid) {
		modifyState = false
		printRecoverStatus(false, Available, block)
		return
	}

	// if already has data
	if block.Status == DataAvailable {
		repairSuccess = true
		printRecoverStatus(false, Available, block)
		return
	}

	// download data
	downloadErr := l.downloadBlock(block)
	if downloadErr == nil {
		repairSuccess = true
		printRecoverStatus(false, DownloadSuccess, block)
		return
	}
	printRecoverStatus(false, DownloadFail, block)

	// repair data
	success := l.sequentialRepair(block, rid, allowDepth)
	if success {
		repairSuccess = true
		printRecoverStatus(false, RepairSuccess, block)
	} else {
		printRecoverStatus(false, RepairFail, block)
	}
}

// parallelRepair repairs a block using muti-threads
func (l *Lattice) parallelRepair(ctx context.Context, block *Block, rid uint) bool {
	pairs := block.GetRecoverPairs()
	if len(pairs) == 0 {
		return false
	}

	finish := make(chan bool)
	counter := 0
	for _, mypair := range pairs {
		util.InfoPrintf(util.Yellow("{Parallel} Left - Index: %d, Parity: %t, Strand: %d\n"+
			"Right - Index: %d, Parity: %t, Strand: %d\n\n"),
			mypair.Left.Index, mypair.Left.IsParity, mypair.Left.Strand,
			mypair.Right.Index, mypair.Right.IsParity, mypair.Right.Strand)

		go func(pair *BlockPair) {
			// tell the caller current func is finished
			success := false
			defer func() { finish <- success }()

			resultChan := make(chan bool, 2)
			go l.parallelRecoverHelper(ctx, pair.Left, rid, resultChan)
			go l.parallelRecoverHelper(ctx, pair.Right, rid, resultChan)

			<-resultChan
			<-resultChan
			leftChunk, err := pair.Left.GetData()
			if err != nil {
				return
			}

			// special case: wrap on itself
			if pair.Left == pair.Right {
				block.SetData(leftChunk, true)
				success = true
				return
			}

			rightChunk, err := pair.Right.GetData()
			if err != nil {
				return
			}

			if block.Recover(leftChunk, rightChunk) == nil {
				success = true
			}
		}(mypair)
	}
	// wait until one recover success, or all routine finishes
	for {
		success := <-finish
		if success {
			return true
		}
		counter++
		if counter >= len(pairs) {
			printRecoverStatus(true, RepairFail, block)
			return false
		}
	}
}

// parallelRecoverHelper is a helper function to recursively do the parallel recovery
func (l *Lattice) parallelRecoverHelper(ctx context.Context, block *Block, rid uint, channel chan bool) {
	var repairSuccess = false
	var modifyState = true
	defer func() {
		if modifyState {
			block.FinishRepair(repairSuccess)
		}
		channel <- true
	}()

	select {
	case <-ctx.Done():
		return
	default:
		// if already has data or already visited
		if !block.StartRepair(ctx, rid) {
			modifyState = false
			return
		}

		// download data
		err := l.downloadBlock(block)
		if err == nil {
			repairSuccess = true
			printRecoverStatus(true, DownloadSuccess, block)
			return
		}
		printRecoverStatus(true, DownloadFail, block)

		// repair data
		success := l.parallelRepair(ctx, block, rid)
		if success {
			repairSuccess = true
			printRecoverStatus(false, RepairSuccess, block)
		} else {
			printRecoverStatus(false, RepairFail, block)
		}
	}
}

// ----------------------------------------------------------------------

// RecoverStatus enums the recover end state of a block
type RecoverStatus int

const (
	DataDownloadSuccess RecoverStatus = iota
	DownloadSuccess
	DownloadFail
	RepairSuccess
	RepairFail
	Available
)

var recoverStatusToString = map[RecoverStatus]string{
	DownloadSuccess: "downloaded successfully",
	DownloadFail:    "downloaded fail",
	RepairSuccess:   "repaired successfully",
	RepairFail:      "repaired fail",
	Available:       "available",
}

var recoverStatusToColor = map[RecoverStatus][]func(...interface{}) string{
	DownloadSuccess: {
		util.White,
		util.Magenta,
	},
	DownloadFail: {
		util.Red,
		util.Red,
	},
	RepairSuccess: {
		util.Green,
		util.Green,
	},
	RepairFail: {
		util.Red,
		util.Red,
	},
	Available: {
		util.White,
		util.Magenta,
	},
}

// printRecoverStatus prints the log for recover stage
func printRecoverStatus(isParallel bool, currStage RecoverStatus, block *Block) {
	var mode string
	if isParallel {
		mode = "Parallel"
	} else {
		mode = "Sequential"
	}

	index := 0
	if block.IsParity {
		index = 1
	}

	color := recoverStatusToColor[currStage][index]
	value := recoverStatusToString[currStage]

	util.LogPrintf(color("{%s} Index: %d, Parity: %t, Strand: %d %s"),
		mode, block.Index, block.IsParity, block.Strand, value)
}
