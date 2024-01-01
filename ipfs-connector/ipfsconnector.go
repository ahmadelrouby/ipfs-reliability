package ipfsconnector

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"ipfs-alpha-entanglement-code/entangler"
	"ipfs-alpha-entanglement-code/util"

	sh "github.com/ipfs/go-ipfs-api"
	dag "github.com/ipfs/go-merkledag"
	unixfs "github.com/ipfs/go-unixfs"
)

// IPFSConnector manages all the interaction with IPFS node
type IPFSConnector struct {
	shell *sh.Shell
}

var DefaultPort = 5001

// CreateIPFSConnector creates a running IPFS node and returns a connector to it
func CreateIPFSConnector(port int, host string) (*IPFSConnector, error) {
	if port == 0 {
		port = DefaultPort
	}

	if host == "" {
		host = "localhost"
	}

	connector := &IPFSConnector{sh.NewShell(fmt.Sprintf("%s:%d", host, port))}

	return connector, nil
}

func (c *IPFSConnector) SetTimeout(duration time.Duration) {
	c.shell.SetTimeout(duration)
}

// AddFile takes the file in the given path and writes it to IPFS network
func (c *IPFSConnector) AddFile(path string) (cid string, err error) {
	file, err := os.Open(path) // TODO catch error if file was not found
	// get the file size
	fileInfo, err := file.Stat()
	util.InfoPrintf("Original File size: %d\n", fileInfo.Size())
	if err != nil {
		return
	}
	defer file.Close()

	return c.shell.Add(file)
}

// AddFileFromMem takes the bytes array and upload it to IPFS network as file
func (c *IPFSConnector) AddFileFromMem(data []byte) (cid string, err error) {
	return c.shell.Add(bytes.NewReader(data))
}

// AddDataFromMem takes the bytes array and upload it to IPFS network as raw leaves
func (c *IPFSConnector) AddDataFromMem(data []byte) (cid string, err error) {
	return c.shell.Add(bytes.NewReader(data), sh.RawLeaves(true))
}

// GetFile takes the file CID and reads it from IPFS network
func (c *IPFSConnector) GetFile(cid string, outputPath string) error {
	return c.shell.Get(cid, outputPath)
}

// GetFileToMem takes the file CID and reads it from IPFS network to memory
func (c *IPFSConnector) GetFileToMem(cid string) ([]byte, error) {
	data, err := c.shell.Cat(cid)
	if err != nil {
		return nil, err
	}

	body, err := io.ReadAll(data)
	if err != nil {
		return nil, err
	}

	return body, nil
}

// AddRawData addes raw block data to IPFS network
func (c *IPFSConnector) AddRawData(chunk []byte) (cid string, err error) {
	return c.shell.BlockPut(chunk, "v0", "sha2-256", -1)
}

// GetRawBlock gets raw block data from IPFS network
func (c *IPFSConnector) GetRawBlock(cid string) (data []byte, err error) {
	return c.shell.BlockGet(cid)
}

func (c *IPFSConnector) GetRawObject(cid string) (*sh.IpfsObject, error) {
	return c.shell.ObjectGet(cid)
}

// GetDagNodeFromRawBytes unmarshals raw bytes into IPFS dagnode
func (c *IPFSConnector) GetDagNodeFromRawBytes(chunk []byte) (dagnode *dag.ProtoNode, err error) {
	dagnode, err = dag.DecodeProtobuf(chunk)
	return
}

// GetFileDataFromDagNode extracts the real file data from IPFS dagnode
func (c *IPFSConnector) GetFileDataFromDagNode(dagnode *dag.ProtoNode) (data []byte, err error) {
	fsn, err := unixfs.FSNodeFromBytes(dagnode.Data())
	if err != nil {
		return
	}

	data = fsn.Data()
	return
}

// GetMerkleTree takes the Merkle tree root CID, constructs the tree and returns the root node
func (c *IPFSConnector) GetMerkleTree(cid string, lattice *entangler.Lattice) (*TreeNode, int, int, error) {
	currIdx := 0
	maxChildren := 0
	var getMerkleNode func(string, int) (*TreeNode, int, error)

	getMerkleNode = func(cid string, currentDepth int) (*TreeNode, int, error) {
		// get the cid node from the IPFS
		rootNodeFile, err := c.shell.ObjectGet(cid) //c.api.ResolveNode(c.ctx, cid)
		if err != nil {
			return nil, 0, err
		}

		rootNode := CreateTreeNode([]byte{})
		rootNode.CID = cid
		rootNode.connector = c
		// update node idx
		rootNode.PreOrderIdx = currIdx
		currIdx++

		if len(rootNodeFile.Links) > maxChildren {
			maxChildren = len(rootNodeFile.Links)
		}

		maxDepth := currentDepth

		// iterate all links that this block points to
		if len(rootNodeFile.Links) > 0 {
			for _, link := range rootNodeFile.Links {
				childNode, tmpDepth, err := getMerkleNode(link.Hash, currentDepth+1)
				if err != nil {
					return nil, currentDepth, err
				}

				if tmpDepth > maxDepth {
					maxDepth = tmpDepth
				}
				rootNode.AddChild(childNode)
			}
		} else {
			rootNode.LeafSize = 1
		}

		return rootNode, maxDepth, nil
	}

	node, depth, err := getMerkleNode(cid, 1)

	return node, maxChildren, depth, err
}

// GetTotalBlocks returns the total number of blocks in the DAG pointed by the cid
func (c *IPFSConnector) GetTotalBlocks(cid string) (int, error) {
	filestate, err := c.shell.FilesStat(context.Background(), "/ipfs/"+cid)
	if err != nil {
		return 0, err
	}
	return filestate.Blocks + 1, nil
}
