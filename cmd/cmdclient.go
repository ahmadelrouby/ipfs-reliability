package cmd

import (
	"encoding/json"
	ipfscluster "ipfs-alpha-entanglement-code/ipfs-cluster"
	ipfsconnector "ipfs-alpha-entanglement-code/ipfs-connector"

	"github.com/spf13/cobra"
	"golang.org/x/xerrors"
)

type Metadata struct {
	Alpha int
	S     int
	P     int

	OriginalFileCID string
	TreeCIDs        []string
	NumBlocks       int // N
	MaxChildren     int // K
	Leaves          int // L
	Depth           int // D

	RootCID string

	DataCIDIndexMap map[string]int
	ParityCIDs      [][]string
}

type Client struct {
	*ipfsconnector.IPFSConnector
	IPFSClusterConnector *ipfscluster.Connector
	*cobra.Command
}

// NewClient creates a new client for futhur use
func NewClient() (client *Client, err error) {
	client = &Client{}
	client.initCmd()

	return client, nil
}

// init ipfs connector for future usage
func (c *Client) InitIPFSConnector() error {
	conn, err := ipfsconnector.CreateIPFSConnector(0)
	if err != nil {
		return xerrors.Errorf("fail to connect to IPFS: %s", err)
	}
	c.IPFSConnector = conn

	return nil
}

// init ipfs cluster connector for future usage
func (c *Client) InitIPFSClusterConnector() error {
	conn, err := ipfscluster.CreateIPFSClusterConnector(0)
	if err != nil {
		return xerrors.Errorf("fail to connect to IPFS Cluster: %s", err)
	}
	c.IPFSClusterConnector = conn

	return nil
}

// AddAndPinAsFile adds a file to IPFS network and pin the file in cluster with a replication factor
// replicate = 0 means use default config in the cluster
func (c *Client) AddAndPinAsFile(data []byte, replicate int) (cid string, err error) {
	// upload file to IPFS network
	cid, err = c.AddFileFromMem(data)
	if err != nil {
		return "", err
	}

	// pin file in cluster
	err = c.IPFSClusterConnector.AddPin(cid, replicate)
	return cid, err
}

// AddAndPinAsRaw adds raw data to IPFS network and pin it in cluster with a replication factor
// replicate = 0 means use default config in the cluster
func (c *Client) AddAndPinAsRaw(data []byte, replicate int) (cid string, err error) {
	// upload raw bytes to IPFS network
	cid, err = c.AddRawData(data)
	if err != nil {
		return "", err
	}

	// pin data in cluster
	err = c.IPFSClusterConnector.AddPin(cid, replicate)
	return cid, err
}

// GetMetaData downloads metafile from IPFS network and returns a metafile object
func (c *Client) GetMetaData(cid string) (metadata *Metadata, err error) {
	data, err := c.GetFileToMem(cid)
	if err != nil {
		return nil, err
	}
	var myMetadata Metadata
	err = json.Unmarshal(data, &myMetadata)
	if err != nil {
		return nil, err
	}

	return &myMetadata, nil
}
