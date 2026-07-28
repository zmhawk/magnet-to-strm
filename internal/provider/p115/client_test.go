package p115

import (
	"encoding/json"
	"errors"
	"testing"

	sdk "github.com/OpenListTeam/115-sdk-go"

	"magnet-to-strm/internal/ingest"
)

func TestOfflineTaskPercentDoneAcceptsIntegerAndFloat(t *testing.T) {
	for _, test := range []struct {
		name string
		raw  string
		want float64
	}{
		{name: "integer", raw: `33`, want: 33},
		{name: "float", raw: `0.33`, want: 0.33},
		{name: "numeric string", raw: `"0.33"`, want: 0.33},
	} {
		t.Run(test.name, func(t *testing.T) {
			var value flexibleFloat64
			if err := json.Unmarshal([]byte(test.raw), &value); err != nil {
				t.Fatal(err)
			}
			if float64(value) != test.want {
				t.Fatalf("got %v, want %v", value, test.want)
			}
		})
	}
}

func TestOfflineTaskKeepsResultAndDeleteFileIDsSeparate(t *testing.T) {
	var response offlineTaskResponse
	if err := json.Unmarshal([]byte(`{
		"info_hash":"905d130c5b469267c96ee66ee4d2922a25877718",
		"file_id":"result-folder",
		"delete_file_id":"source-folder",
		"wp_path_id":"work",
		"status":2
	}`), &response); err != nil {
		t.Fatal(err)
	}
	task := taskFromOfflineResponse(response)
	if task.ResultID != "result-folder" {
		t.Fatalf("ResultID = %q, want result-folder", task.ResultID)
	}
	if task.DeleteFileID != "source-folder" {
		t.Fatalf("DeleteFileID = %q, want source-folder", task.DeleteFileID)
	}
	if task.WPPathID != "work" {
		t.Fatalf("WPPathID = %q, want work", task.WPPathID)
	}
}

func TestOfflineTaskDuplicateErrorRecognizesErrcode10008(t *testing.T) {
	err := offlineTaskDuplicateError(errors.New("request failed"), []byte(
		`{"state":false,"error_msg":"任务已存在，请勿输入重复的链接地址",`+
			`"errno":0,"errtype":"war","errcode":10008,`+
			`"info_hash":"905d130c5b469267c96ee66ee4d2922a25877718"}`,
	))
	if !errors.Is(err, ingest.ErrOfflineTaskExists) {
		t.Fatalf("error = %v, want ErrOfflineTaskExists", err)
	}
}

func TestOfflineTaskDuplicateErrorRecognizesSDKCode10008(t *testing.T) {
	err := offlineTaskDuplicateError(&sdk.Error{
		Code: 10008, Message: "任务已存在，请勿输入重复的链接地址",
	}, nil)
	if !errors.Is(err, ingest.ErrOfflineTaskExists) {
		t.Fatalf("error = %v, want ErrOfflineTaskExists", err)
	}
}

func TestOfflineTaskAddResponseKeepsPerItemResult(t *testing.T) {
	var items []offlineTaskAddResponse
	if err := json.Unmarshal([]byte(`[
		{"state":true,"info_hash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		{"state":false,"info_hash":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		 "errcode":10008,"error_msg":"duplicate"}
	]`), &items); err != nil {
		t.Fatal(err)
	}
	created := items[0].result()
	conflict := items[1].result()
	if !created.Created || created.InfoHash == "" {
		t.Fatalf("created result = %+v", created)
	}
	if conflict.Created || !conflict.Conflict() || conflict.Error != "duplicate" {
		t.Fatalf("conflict result = %+v", conflict)
	}
}
