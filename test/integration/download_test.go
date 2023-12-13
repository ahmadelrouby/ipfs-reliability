package integration

import (
	"fmt"
	"ipfs-alpha-entanglement-code/client"
	"ipfs-alpha-entanglement-code/performance"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func Test_Download(t *testing.T) {
	// util.EnableLogPrint()
	download := func(filepath string, fileCID string, metaCID string, datafilter []int) func(*testing.T) {
		return func(t *testing.T) {
			cl, err := client.NewClient()
			require.NoError(t, err)

			option := client.DownloadOption{
				MetaCID:           metaCID,
				UploadRecoverData: true,
				DataFilter:        datafilter,
			}

			out, err := cl.Download(fileCID, "", option)
			require.NoError(t, err)

			expectedResult, err := os.ReadFile(filepath)
			require.NoError(t, err)
			myResult, err := os.ReadFile(out)
			require.NoError(t, err)
			require.Equal(t, expectedResult, myResult)

			err = os.Remove(out)
			require.NoError(t, err)
		}
	}

	// for _, testcase := range []string{"5MB", "10MB", "20MB", "25MB"} {
	for _, testcase := range []string{"25MB"} {
		filepath := fmt.Sprintf("../data/largefile_%s.txt", testcase)
		info := performance.InfoMap[testcase]
		missingData := make([]int, info.TotalBlock)
		for i := 0; i < info.TotalBlock; i++ {
			missingData[i] = i
		}
		t.Run(testcase, download(filepath, info.FileCID, info.MetaCID, missingData))
	}
}
