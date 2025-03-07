package Server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"ipfs-alpha-entanglement-code/client"
	ipfsconnector "ipfs-alpha-entanglement-code/ipfs-connector"
	"ipfs-alpha-entanglement-code/util"
	"math/rand"
	"net/http"
	"time"
)

func PostJSON(url string, body []byte) (status int, err error) {
	req, err := http.NewRequest("POST", url, bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	fmt.Println("response Status:", resp.Status)
	fmt.Println("response Headers:", resp.Header)

	return resp.StatusCode, nil
}

func (s *Server) RefreshClient() {

	client, err := client.NewClient(s.clusterIP, s.clusterPort, s.ipfsIP, s.ipfsPort)

	if err != nil {
		util.LogPrintf("Error in creating client - %s", err)
	}

	s.client = client
}

func (s *Server) UpdateCoordinatorMetrics(getter *ipfsconnector.IPFSGetter, fileCID string) {

	if getter == nil {
		return
	}

	s.collabData[fileCID].ParityAvailable = getter.ParityAvailable
	s.collabData[fileCID].DataBlocksFetched = getter.DataBlocksFetched
	s.collabData[fileCID].DataBlocksCached = getter.DataBlocksCached
	s.collabData[fileCID].DataBlocksUnavailable = getter.DataBlocksUnavailable
	s.collabData[fileCID].DataBlocksError = getter.DataBlocksError
	s.collabData[fileCID].ParityBlocksFetched = getter.ParityBlocksFetched
	s.collabData[fileCID].ParityBlocksCached = getter.ParityBlocksCached
	s.collabData[fileCID].ParityBlocksUnavailable = getter.ParityBlocksUnavailable
	s.collabData[fileCID].ParityBlocksError = getter.ParityBlocksError

}

// function that takes in a CollaborativeRepairOperationRequest and starts the repair process
func (s *Server) StartCollabRepair(op *CollaborativeRepairOperation) {
	s.RefreshClient()
	s.client.IPFSConnector.SetTimeout(100 * time.Millisecond)
	defer s.client.IPFSConnector.SetTimeout(0)

	util.LogPrintf("Starting collaborative repair for file %s", op.FileCID)

	// check if a repair is already in progress for this file in collabData
	if _, ok := s.collabData[op.FileCID]; ok && s.collabData[op.FileCID].Status == PENDING {
		return
	}

	util.LogPrintf("No repair in progress for file %s, starting new repair", op.FileCID)

	// create a new entry in collabData
	s.collabData[op.FileCID] = &CollaborativeRepairData{
		FileCID:                 op.FileCID,
		MetaCID:                 op.MetaCID,
		Status:                  PENDING,
		StartTime:               time.Now(),
		Depth:                   op.Depth,
		Origin:                  op.Origin,
		Peers:                   make(map[string]*CollabPeerInfo),
		ParityAvailable:         make([]bool, 0),
		DataBlocksFetched:       0,
		DataBlocksCached:        0,
		DataBlocksUnavailable:   0,
		DataBlocksError:         0,
		ParityBlocksFetched:     0,
		ParityBlocksCached:      0,
		ParityBlocksUnavailable: 0,
		ParityBlocksError:       0,
	}

	util.LogPrintf("Created new entry in collabData for file %s", op.FileCID)

	// first repair the intermediate nodes of the tree
	leaves, getter, err := s.client.RetrieveFailedLeaves(op.FileCID, op.MetaCID, op.Depth)

	s.UpdateCoordinatorMetrics(getter, op.FileCID)

	if err != nil {
		util.LogPrintf("Error in retrieving failed leaves for file %s - %s", op.FileCID, err)
		s.collabData[op.FileCID].Status = FAILURE
		s.collabData[op.FileCID].EndTime = time.Now()
		s.ReportMetrics(op.FileCID)
		return
	}

	util.LogPrintf("Retrieved %d failed leaves for file %s", len(leaves), op.FileCID)

	// if there are no failed leaves, then the repair is done
	if len(leaves) == 0 {
		util.LogPrintf("No failed leaves for file %s", op.FileCID)
		s.collabData[op.FileCID].Status = SUCCESS
		s.collabData[op.FileCID].EndTime = time.Now()
		s.ReportMetrics(op.FileCID)
		return
	}

	// if there are failed leaves, we need to get all peers available in the cluster
	_, _, peers, err := s.getAllPeers()

	if err != nil {
		util.LogPrintf("Error in getting all peers for file %s - %s", op.FileCID, err)
		s.collabData[op.FileCID].Status = FAILURE
		s.collabData[op.FileCID].EndTime = time.Now()
		s.ReportMetrics(op.FileCID)
		return
	}

	util.LogPrintf("Retrieved %d peers for file %s", len(peers), op.FileCID)

	// find max number of peers to use for repair
	numPeers := op.NumPeers
	if len(peers) < numPeers {
		numPeers = len(peers)
	}
	if len(leaves) < numPeers {
		numPeers = len(leaves)
	}

	util.LogPrintf("Using %d peers for file %s", numPeers, op.FileCID)

	// shuffle the peerIPs list
	for i := range peers {
		j := rand.Intn(i + 1)
		peers[i], peers[j] = peers[j], peers[i]
	}

	// for this shuffled list, iterate over peers
	// send a request to each of IP/triggerUnitRepair
	// the request should follow the UnitRepairOperationRequest struct
	// each peer should take i*len(leaves)/numPeers to (i+1)*len(leaves)/numPeers leaves
	// if the peer sends back status 200, then move on to the next peer
	// for each peer that sends back status 200, add it to the list of peers that have successfully started
	// if the peer sends back status 400, then retry the request twice before moving on to the next peer

	// prepare the requests in advance
	leavesPerPeer := len(leaves) / numPeers
	requests := make([]*UnitRepairOperationRequest, numPeers)
	jsonRequests := make([][]byte, numPeers)
	for i := 0; i < numPeers; i++ {
		requests[i] = &UnitRepairOperationRequest{
			FileCID:       op.FileCID,
			MetaCID:       op.MetaCID,
			Depth:         op.Depth,
			Origin:        s.address,
			FailedIndices: leaves[i*leavesPerPeer : (i+1)*leavesPerPeer],
		}
		if i == numPeers-1 {
			requests[i].FailedIndices = leaves[i*leavesPerPeer:]
		}
		jsonRequests[i], err = json.Marshal(requests[i])
		if err != nil {
			util.LogPrintf("Error in marshalling request for peer %d - %s", i, err)
			s.collabData[op.FileCID].Status = FAILURE
			s.collabData[op.FileCID].EndTime = time.Now()
			s.ReportMetrics(op.FileCID)
			return
		}
	}

	sentRequests := 0
	i := 0
	for sentRequests < numPeers {
		// send the request to the peer
		status, err := PostJSON("http://"+peers[i]+"/triggerUnitRepair", jsonRequests[i])

		if err == nil && status == 200 {
			// add the peer to the list of peers that have successfully started
			// check if peer already exists in peers
			if _, ok := s.collabData[op.FileCID].Peers[peers[i]]; !ok {
				s.collabData[op.FileCID].Peers[peers[i]] = &CollabPeerInfo{
					Name:                    peers[i],
					StartTime:               time.Now(),
					Status:                  PENDING,
					AllocatedBlocks:         make(map[int]bool),
					ParityAvailable:         make([]bool, 0),
					DataBlocksFetched:       0,
					DataBlocksCached:        0,
					DataBlocksUnavailable:   0,
					DataBlocksError:         0,
					ParityBlocksFetched:     0,
					ParityBlocksCached:      0,
					ParityBlocksUnavailable: 0,
					ParityBlocksError:       0,
				}
			}

			util.LogPrintf("Successfully sent request to peer %s for file %s with %d leaves", peers[i], op.FileCID, len(requests[i].FailedIndices))
			// add the allocated blocks to the peer
			for _, leaf := range requests[i].FailedIndices {
				s.collabData[op.FileCID].Peers[peers[i]].AllocatedBlocks[leaf] = false
			}

			sentRequests++
		}

		// Move in circular fashion as long as we haven't found numPeers peers
		i++
		if i == len(peers) {
			i = 0
		}
	}

	// if we reach here, then we have successfully sent requests to all peers
	// We will wait for each of them to send back an async response to update the status of the repair
	// once all peers have sent back a response, we will check if all leaves have been repaired
}

// function that takes in a UnitRepairOperation and starts the repair process
func (s *Server) StartUnitRepair(op *UnitRepairOperation) {
	s.RefreshClient()
	s.client.IPFSConnector.SetTimeout(100 * time.Millisecond)
	defer s.client.IPFSConnector.SetTimeout(0)

	// get the failedIndices from the request
	// trigger client.RepairFailedLeaves
	// return the result from each of the failedIndices

	util.LogPrintf("Starting unit repair for file %s, with depth %d and %d failed leaves", op.FileCID, op.Depth, len(op.FailedIndices))
	res, getter, err := s.client.RepairFailedLeaves(op.FileCID, op.MetaCID, op.Depth, op.FailedIndices)

	if err != nil {
		util.LogPrintf("Error in repairing failed leaves for file %s - %s", op.FileCID, err)
	}

	util.LogPrintf("Finished unit repair for file %s", op.FileCID)
	for i, r := range res {
		util.LogPrintf("Repair result for leaf %d: %t", i, r)
	}

	// send back the result to the origin
	response := &UnitRepairOperationResponse{
		FileCID:      op.FileCID,
		MetaCID:      op.MetaCID,
		Origin:       s.address,
		RepairStatus: res,
	}

	if getter != nil {
		response.ParityAvailable = getter.ParityAvailable
		response.DataBlocksFetched = getter.DataBlocksFetched
		response.DataBlocksCached = getter.DataBlocksCached
		response.DataBlocksUnavailable = getter.DataBlocksUnavailable
		response.DataBlocksError = getter.DataBlocksError
		response.ParityBlocksFetched = getter.ParityBlocksFetched
		response.ParityBlocksCached = getter.ParityBlocksCached
		response.ParityBlocksUnavailable = getter.ParityBlocksUnavailable
		response.ParityBlocksError = getter.ParityBlocksError
	}

	util.LogPrintf("Sending back response for file %s to %s", op.FileCID, op.Origin)

	jsonResponse, err := json.Marshal(response)
	if err != nil {
		util.LogPrintf("Error in marshalling response for file %s - %s", op.FileCID, err)
		return
	}

	// send the response back to the origin
	PostJSON("http://"+op.Origin+"/reportUnitRepair", jsonResponse)
}

func (s *Server) ReportMetrics(fileCID string) {
	data := s.collabData[fileCID]
	if data == nil {
		return
	}

	jsonResponse, err := json.Marshal(data)
	if err != nil {
		util.LogPrintf("Error in marshalling metrics response for file %s - %s", fileCID, err)
		return
	}

	// send the response back to the disovery/metrics
	PostJSON("http://"+s.discoveryAddress+"/reportMetrics", jsonResponse)
}

func (s *Server) ReportDownloadMetrics(getter *ipfsconnector.IPFSGetter, startTime *time.Time, endTime *time.Time, status RepairStatus) {
	// A non expected failure occured
	if getter == nil {
		return
	}

	metrics := &DownloadMetrics{
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

	jsonResponse, err := json.Marshal(metrics)
	if err != nil {
		util.LogPrintf("Error in marshalling download metrics response - %s", err)
		return
	}

	// send the response back to the disovery/reportDownloadMetrics
	PostJSON("http://"+s.discoveryAddress+"/reportDownloadMetrics", jsonResponse)
}

// function that takes in *UnitRepairDone, updates its corresponding entry in collabData
// and checks if all leaves have been repaired

func (s *Server) ReportUnitRepair(op *UnitRepairDone) {

	util.LogPrintf("Reporting unit repair for file %s", op.FileCID)

	// check if entry exists in collabData
	if _, ok := s.collabData[op.FileCID]; !ok {
		util.LogPrintf("Error in reporting unit repair for file %s - entry does not exist in collabData", op.FileCID)
		return
	}

	util.LogPrintf("Entry exists in collabData for file %s", op.FileCID)

	// check if peer exists in collabData
	if _, ok := s.collabData[op.FileCID].Peers[op.Origin]; !ok {
		util.LogPrintf("Error in reporting unit repair for file %s - peer %s does not exist in collabData", op.FileCID, op.Origin)
		// print all peers in collabData
		util.LogPrintf("Peers in available:")
		for peer := range s.collabData[op.FileCID].Peers {
			util.LogPrintf("Peer %s", peer)
		}
		return
	}

	// update the entry in collabData
	s.collabData[op.FileCID].Peers[op.Origin].EndTime = time.Now()

	util.LogPrintf("Peer %s finished unit repair for file %s with total time of %s", op.Origin, op.FileCID, s.collabData[op.FileCID].Peers[op.Origin].EndTime.Sub(s.collabData[op.FileCID].Peers[op.Origin].StartTime).String())

	if s.collabData[op.FileCID].Peers[op.Origin].ParityAvailable != nil {
		s.collabData[op.FileCID].Peers[op.Origin].ParityAvailable = op.ParityAvailable
	}
	s.collabData[op.FileCID].Peers[op.Origin].DataBlocksFetched = op.DataBlocksFetched
	s.collabData[op.FileCID].Peers[op.Origin].DataBlocksCached = op.DataBlocksCached
	s.collabData[op.FileCID].Peers[op.Origin].DataBlocksUnavailable = op.DataBlocksUnavailable
	s.collabData[op.FileCID].Peers[op.Origin].DataBlocksError = op.DataBlocksError
	s.collabData[op.FileCID].Peers[op.Origin].ParityBlocksFetched = op.ParityBlocksFetched
	s.collabData[op.FileCID].Peers[op.Origin].ParityBlocksCached = op.ParityBlocksCached
	s.collabData[op.FileCID].Peers[op.Origin].ParityBlocksUnavailable = op.ParityBlocksUnavailable

	repaired := 0
	success := true
	// check if all leaves have been repaired
	for leaf, status := range op.RepairStatus {
		success = success && status
		if status {
			s.collabData[op.FileCID].Peers[op.Origin].AllocatedBlocks[leaf] = true
			repaired++
		}
	}

	util.LogPrintf("Peer %s repaired %d/%d leaves for file %s", op.Origin, repaired, len(op.RepairStatus), op.FileCID)

	if success {
		s.collabData[op.FileCID].Peers[op.Origin].Status = SUCCESS
	} else {
		s.collabData[op.FileCID].Peers[op.Origin].Status = FAILURE
	}

	// check if all peers have finished
	allPeersDone := true
	allPeersSucceeded := true
	for _, peer := range s.collabData[op.FileCID].Peers {
		allPeersDone = allPeersDone && (peer.Status != PENDING)
		allPeersSucceeded = allPeersSucceeded && (peer.Status == SUCCESS)
	}

	if allPeersDone {
		// update time and status of collabData
		s.collabData[op.FileCID].EndTime = time.Now()
		if allPeersSucceeded {
			s.collabData[op.FileCID].Status = SUCCESS
		} else {
			s.collabData[op.FileCID].Status = FAILURE
		}

		util.LogPrintf("All peers finished unit repair for file %s with total time of %s", op.FileCID, s.collabData[op.FileCID].EndTime.Sub(s.collabData[op.FileCID].StartTime).String())
		s.ReportMetrics(op.FileCID)
		// check if there's origin for this file

		if s.collabData[op.FileCID].Origin == "" {
			util.LogPrintf("No origin for collaborative repair of file %s, so no need to report back", op.FileCID)
			return
		}

		// send back the result to the origin
		response := &CollaborativeRepairOperationResponse{
			FileCID:      op.FileCID,
			MetaCID:      op.MetaCID,
			Origin:       s.address,
			RepairStatus: success,
		}

		jsonResponse, err := json.Marshal(response)
		if err != nil {
			util.LogPrintf("Error in marshalling response for file %s - %s", op.FileCID, err)
			return
		}

		// send the response back to the origin
		PostJSON("http://"+s.collabData[op.FileCID].Origin+"/reportCollabRepair", jsonResponse)

	}

}

// function that takes in a StrandRepairOperation and starts the repair process
func (s *Server) StartStrandRepair(op *StrandRepairOperation) {
	s.RefreshClient()
	s.client.IPFSConnector.SetTimeout(100 * time.Millisecond)
	defer s.client.IPFSConnector.SetTimeout(0)
	// get the failedIndices from the request
	// trigger client.RepairFailedLeaves
	// return the result from each of the failedIndices

	// Check if same file is being repaired
	// We'll assume that only one strand can be repaired at a time
	if _, ok := s.strandData[op.FileCID]; ok && s.strandData[op.FileCID].Status == PENDING {
		return
	}

	// create a new entry in strandData
	s.strandData[op.FileCID] = &StrandRepairData{
		FileCID:   op.FileCID,
		MetaCID:   op.MetaCID,
		Strand:    op.Strand,
		Status:    PENDING,
		Depth:     op.Depth,
		StartTime: time.Now(),
	}

	// first create a new collab repair operation
	newOp := &CollaborativeRepairOperation{
		FileCID:  op.FileCID,
		MetaCID:  op.MetaCID,
		Depth:    op.Depth,
		Origin:   s.address,
		NumPeers: 3, // should be variable but not as important here
	}

	// We need to make sure that file is data is actually available to be able to repair this strand
	s.collabOps <- newOp
}

// function that takes in *CollabOperationDone, updates its corresponding entry in strandData
func (s *Server) ContinueStrandRepair(op *CollaborativeRepairDone) {

	// if we're not trying to repair any strands we could just ignore
	if _, ok := s.strandData[op.FileCID]; !ok {
		s.resetMonitorFile(op.FileCID, true)
		return
	}

	// if the strand we're repairing somehow already finished then we can just ignore
	if s.strandData[op.FileCID].Status != PENDING {
		s.resetMonitorFile(op.FileCID, false)
		return
	}

	// if the collab repair failed then we can need to fail the strand repair
	if !op.RepairStatus {
		s.strandData[op.FileCID].Status = FAILURE
		s.strandData[op.FileCID].EndTime = time.Now()
		return
	}

	// if the collab repair succeeded then we can continue with the strand repair
	// we just need to trigger client.RepairStrand
	err := s.client.RepairStrand(op.FileCID, op.MetaCID, s.strandData[op.FileCID].Strand)

	if err != nil {
		util.LogPrintf("Error in repairing strand for file %s - %s", op.FileCID, err)
		s.strandData[op.FileCID].Status = FAILURE
		s.strandData[op.FileCID].EndTime = time.Now()
		s.resetMonitorFile(op.FileCID, true)

		return
	}

	// if we reach here, then the strand repair succeeded
	s.strandData[op.FileCID].Status = SUCCESS
	s.strandData[op.FileCID].EndTime = time.Now()

	s.resetMonitorFile(op.FileCID, false)
}
