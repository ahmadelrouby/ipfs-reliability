package client

import (
	"bytes"
	"encoding/json"
	"ipfs-alpha-entanglement-code/entangler"
	ipfsconnector "ipfs-alpha-entanglement-code/ipfs-connector"
	"ipfs-alpha-entanglement-code/util"
	"os"
	"time"

	"golang.org/x/xerrors"
)

type DownloadOption struct {
	MetaCID           string
	UploadRecoverData bool
	DataFilter        []int
}

// directDownload interacts directly with IPFS. It fails when any data is missing
func (c *Client) directCountDownload(rootCID string) (int, error) {
	count := 0

	var walker func(string)
	walker = func(nodeCID string) {
		raw_node, err := c.GetRawObject(nodeCID)
		if err != nil {
			return
		}
		count += 1
		// populate the node with data and links if exists
		if len(raw_node.Links) > 0 {
			for _, dag_child := range raw_node.Links {
				walker(dag_child.Hash)
			}
		}
	}

	walker(rootCID)
	return count, nil
}

func (c *Client) DownloadCount(rootCID string, metaCID string, depth uint) (*ipfsconnector.IPFSGetter, int, error) {

	/* direct downloading if no metafile provided or depth is provided as 1 */
	if len(metaCID) == 0 || depth <= 1 {
		cnt, err := c.directCountDownload(rootCID)
		return nil, cnt, err
	}

	option := DownloadOption{
		MetaCID:           metaCID,
		UploadRecoverData: false,
		DataFilter:        []int{},
	}

	_, getter, cnt, err := c.metaDownload(rootCID, option, depth, false)
	return getter, cnt, err
}

// Download download the original file, repair it if metadata is provided
func (c *Client) Download(rootCID string, path string, option DownloadOption, depth uint) ([]byte, *ipfsconnector.IPFSGetter, error) {
	// err = c.InitIPFSConnector()
	// if err != nil {
	// 	return "", err
	// }

	/* direct downloading if no metafile provided or depth is provided as 1 */
	if len(option.MetaCID) == 0 || depth <= 1 {
		return c.directDownload(rootCID)
	}

	data, getter, _, err := c.metaDownload(rootCID, option, depth, true)
	return data, getter, err
}

// directDownload interacts directly with IPFS. It fails when any data is missing
func (c *Client) directDownload(rootCID string) ([]byte, *ipfsconnector.IPFSGetter, error) {
	// try to down original file using given rootCID (i.e. no metafile)
	data, err := c.GetFileToMem(rootCID)
	if err != nil {
		return nil, nil, xerrors.Errorf("fail to download original file: %s", err)
	}
	util.LogPrintf("Finish downloading file (no recovery)")

	return data, nil, nil
}

// downloadAndRecover interacts with IPFS through lattice, It launches recovery if any data is missing
func (c *Client) downloadAndRecover(lattice *entangler.Lattice, metaData *Metadata,
	option DownloadOption, tree *ipfsconnector.EmptyTreeNode, failOnError bool) (data []byte, repaired bool, count int, err error) {

	count = 0
	data = []byte{}
	repaired = false
	var walker func(*ipfsconnector.EmptyTreeNode) error
	walker = func(node *ipfsconnector.EmptyTreeNode) (err error) {
		util.LogPrintf("Downloading chunk with lattice index %d and preorder index %d", node.LatticeIdx, node.PreOrderIdx)
		chunk, hasRepaired, err := lattice.GetChunk(node.LatticeIdx + 1)
		if err != nil {
			return xerrors.Errorf("fail to recover chunk with CID: %s - %s", node.CID, err)
		}

		count += 1
		// upload missing chunk back to the network if allowed
		if hasRepaired {
			// Problem: does trimming zero always works?
			chunk = bytes.Trim(chunk, "\x00")
			err = c.dataReupload(chunk, node.CID, option.UploadRecoverData)
			if err != nil {
				return err
			}
		}
		repaired = repaired || hasRepaired

		// unmarshal and iterate
		dagNode, err := c.GetDagNodeFromRawBytes(chunk)
		if err != nil {
			return xerrors.Errorf("fail to parse raw data: %s", err)
		}
		links := dagNode.Links()

		if len(links) != len(node.Children) {
			return xerrors.Errorf("number of links mismatch: %d expected but %d provided", len(node.Children), len(links))
		}

		for i, link := range links {
			node.Children[i].CID = link.Cid.String()
			err = walker(node.Children[i])
			if err != nil && failOnError {
				return err
			}
		}

		if len(links) == 0 {
			fileChunkData, err := c.GetFileDataFromDagNode(dagNode)
			if err != nil {
				return xerrors.Errorf("fail to parse file data: %s", err)
			}
			data = append(data, fileChunkData...)
		}
		return err
	}
	err = walker(tree)
	return data, repaired, count, err
}

// metaDownload download metadata for recovery usage
func (c *Client) metaDownload(rootCID string, option DownloadOption, depth uint, failOnError bool) ([]byte, *ipfsconnector.IPFSGetter, int, error) {
	/* download metafile */
	metaData, err := c.GetMetaData(option.MetaCID)
	if err != nil {
		return nil, nil, 0, xerrors.Errorf("fail to download metaData: %s", err)
	}

	// Construct empty tree
	merkleTree, child_parent_index_map, index_node_map, err := ipfsconnector.ConstructTree(metaData.Leaves, metaData.MaxChildren, metaData.Depth, metaData.NumBlocks, metaData.S, metaData.P)

	if err != nil {
		return nil, nil, 0, xerrors.Errorf("fail to construct tree: %s", err)
	}

	merkleTree.CID = metaData.OriginalFileCID

	// for each treeCid, create a new parity tree and indices_map
	parityTrees := make([]*ipfsconnector.ParityTreeNode, len(metaData.TreeCIDs))
	parityIndexMap := make([]map[int]*ipfsconnector.ParityTreeNode, len(metaData.TreeCIDs))

	// Calculate parity tree number of leaves based on the following:
	//TODO: Make these numbers global and initialize them once!
	L_parity := (metaData.NumBlocks*262158 + 262143) / 262144
	K_parity := metaData.MaxParityChildren

	for i, treeCID := range metaData.TreeCIDs {
		curr_tree, curr_map := ipfsconnector.CreateParityTree(L_parity, K_parity)
		parityTrees[i], parityIndexMap[i] = curr_tree, curr_map
		parityTrees[i].CID = treeCID
	}

	/* create lattice */
	// create getter
	getter := ipfsconnector.CreateIPFSGetter(c.IPFSConnector, metaData.DataCIDIndexMap, metaData.ParityCIDs, metaData.OriginalFileCID, metaData.TreeCIDs, metaData.NumBlocks, merkleTree, child_parent_index_map, index_node_map, parityTrees, parityIndexMap)
	if len(option.DataFilter) > 0 {
		getter.DataFilter = make(map[int]struct{}, len(option.DataFilter))
		for _, index := range option.DataFilter {
			getter.DataFilter[index] = struct{}{}
		}
	}

	// create lattice
	lattice := entangler.NewLattice(metaData.Alpha, metaData.S, metaData.P, metaData.NumBlocks, getter, depth)
	lattice.Init()

	/* download & recover file from IPFS */
	data, repaired, count, errDownload := c.downloadAndRecover(lattice, metaData, option, merkleTree, failOnError)
	if errDownload != nil {
		err = errDownload
		return nil, getter, count, xerrors.Errorf("fail to download and recover file: %s", err)
	}

	if repaired {
		util.LogPrintf("Finish downloading file (recovered)")
	} else {
		util.LogPrintf("Finish downloading file (no recovery)")
	}

	/* write to file in the given path */
	return data, getter, count, nil
}

// dataReupload re-uploads the recovered data back to IPFS
func (c *Client) dataReupload(chunk []byte, cid string, allow bool) error {
	if !allow {
		return nil
	}

	uploadCID, err := c.AddRawData(chunk)
	if err != nil {
		return xerrors.Errorf("fail to upload the repaired chunk to IPFS: %s", err)
	}
	if uploadCID != cid {
		return xerrors.Errorf("incorrect CID of the repaired chunk. Expected: %s, Got: %s", cid, uploadCID)
	}
	return nil
}

// dataReupload re-uploads the recovered data back to IPFS
func (c *Client) dataReuploadNoCheck(chunk []byte, allow bool) error {
	if !allow {
		return nil
	}

	_, err := c.AddRawData(chunk)
	if err != nil {
		return xerrors.Errorf("fail to upload the repaired chunk to IPFS: %s", err)
	}

	return nil
}

// writeFile writes the recovered data to the file at the output path
func WriteFile(rootCID string, path string, data []byte) (out string, err error) {
	if len(path) == 0 {
		out = rootCID
	} else {
		out = path
	}

	err = os.WriteFile(out, data, 0600)

	return out, err
}

type RepairStatus int

const (
	SUCCESS RepairStatus = iota
	FAILURE
)

type DownloadMetrics struct {
	StartTime               *time.Time   `json:"startTime"`
	EndTime                 *time.Time   `json:"endTime"`
	Status                  RepairStatus `json:"status"`
	ParityAvailable         []bool       `json:"parityAvailable"`
	DataBlocksFetched       int          `json:"dataBlocksFetched"`
	DataBlocksCached        int          `json:"dataBlocksCached"`
	DataBlocksUnavailable   int          `json:"dataBlocksUnavailable"`
	DataBlocksError         int          `json:"dataBlocksError"`
	ParityBlocksFetched     int          `json:"parityBlocksFetched"`
	ParityBlocksCached      int          `json:"parityBlocksCached"`
	ParityBlocksUnavailable int          `json:"parityBlocksUnavailable"`
	ParityBlocksError       int          `json:"parityBlocksError"`
}

func WriteMetrics(path string, getter *ipfsconnector.IPFSGetter, startTime *time.Time, endTime *time.Time, status RepairStatus) (out string, err error) {
	if len(path) == 0 {
		out = "metrics.json"
	} else {
		out = path
	}

	if getter == nil {
		return "", xerrors.Errorf("getter is nil")
	}

	metrics := DownloadMetrics{
		StartTime:               startTime,
		EndTime:                 endTime,
		Status:                  status,
		ParityAvailable:         getter.ParityAvailable,
		DataBlocksFetched:       getter.DataBlocksFetched,
		DataBlocksCached:        getter.DataBlocksCached,
		DataBlocksUnavailable:   getter.DataBlocksUnavailable,
		DataBlocksError:         getter.DataBlocksError,
		ParityBlocksFetched:     getter.ParityBlocksFetched,
		ParityBlocksCached:      getter.ParityBlocksCached,
		ParityBlocksUnavailable: getter.ParityBlocksUnavailable,
		ParityBlocksError:       getter.ParityBlocksError,
	}

	jsonBody, err := json.Marshal(metrics)

	if err != nil {
		return "", err
	}

	err = os.WriteFile(out, jsonBody, 0600)

	return out, err
}
