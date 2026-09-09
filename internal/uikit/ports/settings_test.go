package ports

import "testing"

var (
	_ GeneralEdit = SetSyncIncludeThinking{}
	_ GeneralEdit = SetSyncIncludeToolIO{}
	_ GeneralEdit = SetSyncStreamAssistant{}
)

func TestSyncGeneralEditVariantsSatisfyTheClosedUnion(t *testing.T) {
	edits := []GeneralEdit{
		SetSyncIncludeThinking{On: true},
		SetSyncIncludeToolIO{On: true},
		SetSyncStreamAssistant{On: true},
	}
	for _, e := range edits {
		e.isGeneralEdit()
	}
}
