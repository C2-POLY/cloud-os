-- +goose Up
-- An SMB write whose storing fails is kept in the trash (#111,
-- docs/architecture/smb.md, « Un enregistrement qui échoue »).
--
-- 1. A trash item says why it is there when nobody deleted it: 'smb_not_saved'
--    is the content of an SMB write the box could not store (the database
--    down, or the name taken). NULL is an item someone deleted.
ALTER TABLE trash ADD COLUMN reason text CHECK (reason IN ('smb_not_saved'));

-- 2. Moving a rescued content into the trash is journaled in moves as op
--    'rescue': from_path is its id under .cloudos/rescue/<owner>, to_path the
--    path it was written at, trash_id its new trash item. RecoverMoves and
--    SettleRescued finish or undo it.
ALTER TABLE moves DROP CONSTRAINT moves_op_check;
ALTER TABLE moves ADD CONSTRAINT moves_op_check CHECK (op IN ('move', 'trash', 'restore', 'version', 'replace', 'rescue'));

-- 3. The smb service's role puts the rescued content in the trash itself
--    (storage.SettleRescued): a trash item with its reason, and the index row
--    of a file born in the trash. smbfs.CheckRole knows these two columns.
GRANT INSERT (trash_id) ON files TO cloudos_smb;
GRANT INSERT (reason) ON trash TO cloudos_smb;

-- +goose Down
-- Refuse to drop the operation a journal entry still stands for.
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM moves WHERE op = 'rescue') THEN
        RAISE EXCEPTION 'start the core once (RecoverMoves settles the rescues left) before undoing migration 29';
    END IF;
END
$$;
-- +goose StatementEnd

-- No row and no file is touched: the items already kept stay in the trash,
-- restorable, and only lose the reason they show.
REVOKE INSERT (trash_id) ON files FROM cloudos_smb;
REVOKE INSERT (reason) ON trash FROM cloudos_smb;

ALTER TABLE moves DROP CONSTRAINT moves_op_check;
ALTER TABLE moves ADD CONSTRAINT moves_op_check CHECK (op IN ('move', 'trash', 'restore', 'version', 'replace'));

ALTER TABLE trash DROP COLUMN reason;
