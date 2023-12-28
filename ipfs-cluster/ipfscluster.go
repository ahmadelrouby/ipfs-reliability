package ipfscluster

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

var DefaultPort = 9094

type Connector struct {
	url        string
	selfID     string
	peerIDs    []string
	currentIdx int
	peers      map[string]string
}

// CreateIPFSClusterConnector is the constructor of IPFSClusterConnector
func CreateIPFSClusterConnector(port int, host string) (*Connector, error) {
	if port == 0 {
		port = DefaultPort
	}

	if host == "" {
		host = "localhost"
	}

	conn := Connector{url: fmt.Sprintf("http://%s:%d", host, port)}
	conn.peers = make(map[string]string)
	_, err := conn.PeerInfo()
	if err != nil {
		return nil, err
	}
	_, err = conn.PeerLs()
	if err != nil {
		return nil, err
	}
	return &conn, nil
}

// PeerInfo list the info about the cluster peers
func (c *Connector) PeerInfo() (string, error) {
	/* Return the connected peer info
	For the moment, only returns the name of the connected peer */
	resp, err := http.Get(c.url + "/id")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	decoder := json.NewDecoder(resp.Body)
	var info map[string]interface{}
	if err = decoder.Decode(&info); err != nil {
		panic(err)
	}

	selfID, ok := info["id"].(string)
	if !ok {
		panic("ID field does not exist!")
	}
	c.selfID = selfID

	selfName, ok := info["peername"].(string)
	if !ok {
		panic("peername field does not exist!")
	}
	return selfName, nil
}

// PeerLs list the number of peers that are inside the cluster
func (c *Connector) PeerLs() (int, error) {
	/* List all peers inside the IPFS cluster
	For the moment, only returns the number of peers */
	resp, err := http.Get(c.url + "/peers")
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	var peersInfo []map[string]interface{}
	decoder := json.NewDecoder(resp.Body)
	for decoder.More() {
		var info map[string]interface{}
		if err = decoder.Decode(&info); err != nil {
			panic(err)
		}
		peersInfo = append(peersInfo, info)
		if info["id"].(string) != c.selfID {
			c.peers[info["id"].(string)] = info["peername"].(string)
			c.peerIDs = append(c.peerIDs, info["id"].(string))
		}
	}

	return len(peersInfo), nil
}

func (c *Connector) GetAllPeers() map[string]string {
	return c.peers
}

func (c *Connector) GetLatestPeers() map[string]string {
	c.PeerLs()
	return c.peers
}

func (c *Connector) GetPeerIDs() []string {
	return c.peerIDs
}

func (c *Connector) GetPeerName(peerID string) string {
	if peerID == c.selfID {
		name, err := c.PeerInfo() // FIXME can also save c.selfName while running PeerInfo()
		if err != nil {
			return ""
		}
		return name
	}
	return c.peers[peerID]
}

// PinStatus check the status of the specified cid, if the CID is not given, it will
// show all CIDs that are inside the ipfs cluster
func (c *Connector) PinStatus(cid string) (string, error) {
	/* Check the pin status of all CIDs or a specific CID
	For the moment, only checks the number of pin peers */
	var statusURL string
	var pinStatus string
	if cid == "" {
		statusURL = c.url + "/pins"
	} else {
		statusURL = c.url + "/pins/" + cid
	}

	resp, err := http.Get(statusURL)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var pinInfo []map[string]interface{}
	decoder := json.NewDecoder(resp.Body)
	for decoder.More() {
		var status map[string]interface{}
		if err = decoder.Decode(&status); err != nil {
			panic(err)
		}
		pinInfo = append(pinInfo, status)
	}

	for _, status := range pinInfo {
		var statusMap = status["peer_map"].(map[string]interface{})
		var pinCount int
		for key := range statusMap {
			if statusMap[key].(map[string]interface{})["status"].(string) == "pinned" {
				pinCount++
			}
		}
		pinStatus += fmt.Sprintf("%s pinned by %d peers.\n", status["cid"].(string), pinCount)
	}
	pinStatus = fmt.Sprintf("\nTotal number of pins: %d\n", len(pinInfo)) + pinStatus

	return pinStatus, err
}

// Returns the peer names of the peers that are pinning the specified CID
func (c *Connector) GetPinAllocations(cid string) ([]string, error) {
	/* Check the pin status of all CIDs or a specific CID
	For the moment, only checks the number of pin peers */
	statusURL := c.url + "/pins/" + cid

	resp, err := http.Get(statusURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var pinInfo map[string]interface{}
	decoder := json.NewDecoder(resp.Body)

	if err = decoder.Decode(&pinInfo); err != nil {
		panic(err)
	}

	allocations, ok := pinInfo["allocations"].([]interface{})
	if !ok {
		fmt.Println("Error: unable to find or assert allocations as an array")
		return nil, err
	}

	var peerNames []string
	for _, allocation := range allocations {
		if peerID, ok := allocation.(string); ok {
			peerNames = append(peerNames, c.GetPeerName(peerID))
		}
	}

	return peerNames, nil

}

// AddPin add the specified CID to the ipfs cluster, with the specified replication factor,
// the default behavior is recursive, which means pinning all content that is beneath the CID
// "mode" can be "direct" or "recursive"
func (c *Connector) AddPin(cid string, replicationFactor int) error {
	/* Add a new CID to the cluster,  it uses the default replication
	factor that is specified in the CLUSTER configuration file */
	peerID := c.peerIDs[c.currentIdx]
	c.currentIdx = (c.currentIdx + 1) % len(c.peerIDs)
	postURL := fmt.Sprintf("%s/pins/ipfs/%s?mode=recursive&name=&replication-max="+
		"%d&replication-min=%d&shard-size=0&user-allocations=%s",
		c.url, cid, replicationFactor, replicationFactor, peerID)
	resp, err := http.PostForm(postURL, nil)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return err
}

func (c *Connector) AddPinDirect(cid string, replicationFactor int) error {
	/* Add a new CID to the cluster,  it uses the default replication
	factor that is specified in the CLUSTER configuration file */
	peerID := c.peerIDs[c.currentIdx]
	c.currentIdx = (c.currentIdx + 1) % len(c.peerIDs)
	postURL := fmt.Sprintf("%s/pins/ipfs/%s?mode=direct&name=&replication-max="+
		"%d&replication-min=%d&shard-size=0&user-allocations=%s",
		c.url, cid, replicationFactor, replicationFactor, peerID)
	resp, err := http.PostForm(postURL, nil)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return err
}

// PeerLoad checks the load balance of the cluster, namely how many blocks is stored on each
// cluster peer
func (c *Connector) PeerLoad() (string, error) {
	min := func(a, b int) int {
		if a < b {
			return a
		}
		return b
	}

	max := func(a, b int) int {
		if a > b {
			return a
		}
		return b
	}

	statusURL := c.url + "/pins"
	resp, err := http.Get(statusURL)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var totBlocks int
	peerInfo := make(map[string]int)
	decoder := json.NewDecoder(resp.Body)
	for decoder.More() {
		var status map[string]interface{}
		if err = decoder.Decode(&status); err != nil {
			panic(err)
		}
		var statusMap = status["peer_map"].(map[string]interface{})
		for key := range statusMap {
			pinStatus, ok := statusMap[key].(map[string]interface{})["status"].(string)
			if !ok {
				panic("status field does not exist!")
			}
			if pinStatus == "pinned" {
				peerInfo[key]++
				totBlocks++
			}
		}
	}

	var peerLoad string
	minBlocks := totBlocks
	maxBlocks := 0
	var blocks []string
	for key := range peerInfo {
		minBlocks = min(minBlocks, peerInfo[key])
		maxBlocks = max(maxBlocks, peerInfo[key])
		blocks = append(blocks, strconv.Itoa(peerInfo[key]))
	}

	peerLoad += fmt.Sprintf("\nTotal blocks in the cluster: %d\n", totBlocks)
	peerLoad += fmt.Sprintf("Min blocks: %d, Max blocks: %d\n", minBlocks, maxBlocks)
	peerLoad += fmt.Sprintf("Detailed blocks info: %s", strings.Join(blocks, ", "))

	return peerLoad, nil
}
