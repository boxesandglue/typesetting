package opentype

import (
	"bytes"
	"testing"

	tu "github.com/boxesandglue/typesetting/testutils"
	td "github.com/go-text/typesetting-utils/opentype"
)

func TestWrite(t *testing.T) {
	for _, filename := range tu.Filenames(t, "common") {
		f, err := td.Files.ReadFile(filename)
		tu.AssertNoErr(t, err)

		font, err := NewLoader(bytes.NewReader(f))
		tu.AssertNoErr(t, err)

		tags := font.Tables()
		tables := make([]Table, len(tags))
		for i, tag := range tags {
			tables[i].Tag = tag
			tables[i].Content, err = font.RawTable(tag)
			tu.AssertNoErr(t, err)
		}

		content := WriteTTF(tables)
		font2, err := NewLoader(bytes.NewReader(content))
		tu.AssertNoErr(t, err)

		for _, table := range tables {
			t2, err := font2.RawTable(table.Tag)
			tu.AssertNoErr(t, err)

			tu.Assert(t, bytes.Equal(table.Content, t2))
		}
	}
}
