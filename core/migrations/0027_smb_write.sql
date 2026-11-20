-- +goose Up
-- The drive becomes writable over SMB (#92, docs/architecture/smb.md).
--
-- 1. MoveReplace, a rename that replaces a file, is journaled in moves as
--    op 'replace': from_path is renamed onto to_path, whose content before
--    is kept as version version_id. RecoverMoves finishes or undoes it.
ALTER TABLE moves DROP CONSTRAINT moves_op_check;
ALTER TABLE moves ADD CONSTRAINT moves_op_check CHECK (op IN ('move', 'trash', 'restore', 'version', 'replace'));

-- 2. The smb service's role, cloudos_smb (migration 0026), gets what storage
--    needs for the SMB writes and nothing more: Stage and Commit (Put with
--    Replace), Mkdir, Trash, Move, MoveReplace and SetModTime, the analysis
--    tasks and the activity line a stored file queues. Column grants wherever
--    a table holds anything else. smbfs.CheckRole refuses any wider role.
--
-- The index: new rows (Commit, Mkdir and the folders above them), their
-- content and date (Commit over a file, SetModTime), their path (Move), their
-- trash mark (Trash). DELETE is MoveReplace's: the source's row goes once its
-- content is the target's.
GRANT INSERT (owner, path, kind, size, mime, sha256, modified_at),
      UPDATE (path, size, mime, sha256, modified_at, indexed_at, trash_id),
      DELETE ON files TO cloudos_smb;
-- Versions: a replaced content is kept (Commit, MoveReplace); the retention
-- of the file just replaced, and the undo of a replace that failed, delete rows.
GRANT SELECT,
      INSERT (file_id, owner, size, mime, sha256, modified_at),
      DELETE ON file_versions TO cloudos_smb;
-- The journal: an intention is written before the disk is touched and
-- deleted once the operation is indexed.
GRANT SELECT,
      INSERT (owner, from_path, to_path, op, trash_id, version_id),
      DELETE ON moves TO cloudos_smb;
-- The trash: Trash adds an item, and deletes it if the disk refused the move.
GRANT SELECT,
      INSERT (owner, path, kind, size, expires_at),
      DELETE ON trash TO cloudos_smb;
-- The analysis tasks a new content queues (thumbnail, metadata, text, OCR,
-- photo vector), after checking it has none yet. ON CONFLICT reads the
-- columns of the deduplicating index (dedupe_key, state); RETURNING reads id.
-- Neither a payload nor an error of the queue is readable.
GRANT SELECT (id, dedupe_key, state),
      INSERT (kind, payload, priority, max_attempts, dedupe_key) ON tasks TO cloudos_smb;
GRANT SELECT (sha256, size) ON thumbnails TO cloudos_smb;
GRANT SELECT (sha256) ON media_meta TO cloudos_smb;
GRANT SELECT (sha256) ON document_text TO cloudos_smb;
GRANT SELECT (name, version) ON embedding_models TO cloudos_smb;
GRANT SELECT (sha256, version) ON photo_vectors_siglip2_b16_224 TO cloudos_smb;
-- The activity journal: "added", "replaced", "trashed".
GRANT INSERT (owner, kind, path) ON activity TO cloudos_smb;

-- +goose Down
-- Refuse to drop the operation a journal entry still stands for.
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM moves WHERE op = 'replace') THEN
        RAISE EXCEPTION 'start the core once (RecoverMoves settles the replaces left) before undoing migration 27';
    END IF;
END
$$;
-- +goose StatementEnd

-- Only rights go: no row, no file is touched. Revoking a table privilege
-- revokes the column privileges with it; SELECT on files is migration 26's.
REVOKE INSERT, UPDATE, DELETE ON files FROM cloudos_smb;
REVOKE ALL ON file_versions, moves, trash, tasks, thumbnails, media_meta, document_text,
              embedding_models, photo_vectors_siglip2_b16_224, activity FROM cloudos_smb;

ALTER TABLE moves DROP CONSTRAINT moves_op_check;
ALTER TABLE moves ADD CONSTRAINT moves_op_check CHECK (op IN ('move', 'trash', 'restore', 'version'));
