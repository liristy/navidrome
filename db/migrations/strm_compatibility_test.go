package migrations

import (
	"context"
	"database/sql"

	_ "github.com/mattn/go-sqlite3"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("STRM compatibility migration", func() {
	It("adds missing fields and preserves fields created by the legacy fork", func() {
		ctx := context.Background()
		db, err := sql.Open("sqlite3", ":memory:")
		Expect(err).ToNot(HaveOccurred())
		DeferCleanup(func() { _ = db.Close() })
		_, err = db.Exec(`
			CREATE TABLE property (id TEXT PRIMARY KEY, value TEXT);
			CREATE TABLE media_file (path TEXT NOT NULL);
			INSERT INTO media_file(path) VALUES ('album/song.strm'), ('album/song.flac');
		`)
		Expect(err).ToNot(HaveOccurred())

		tx, err := db.BeginTx(ctx, nil)
		Expect(err).ToNot(HaveOccurred())
		Expect(upAddStrmCompatibilityFields(ctx, tx)).To(Succeed())
		Expect(tx.Commit()).To(Succeed())

		var isStrm bool
		var target string
		Expect(db.QueryRow("SELECT is_strm, strm_target FROM media_file WHERE path LIKE '%.strm'").Scan(&isStrm, &target)).To(Succeed())
		Expect(isStrm).To(BeTrue())
		Expect(target).To(BeEmpty())

		_, err = db.Exec("UPDATE media_file SET strm_target='/CloudNAS/song.flac' WHERE is_strm")
		Expect(err).ToNot(HaveOccurred())
		tx, err = db.BeginTx(ctx, nil)
		Expect(err).ToNot(HaveOccurred())
		Expect(upAddStrmCompatibilityFields(ctx, tx)).To(Succeed())
		Expect(tx.Commit()).To(Succeed())
		Expect(db.QueryRow("SELECT strm_target FROM media_file WHERE is_strm").Scan(&target)).To(Succeed())
		Expect(target).To(Equal("/CloudNAS/song.flac"))
	})
})
