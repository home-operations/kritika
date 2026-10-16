-- What the forge last said of who can read the repository: public, private
-- or internal, as GitHub names them, and '' until it says. The next listing
-- of the App's repositories fills it in. The value is not checked against
-- those three: one GitHub adds would otherwise fail the webhook, or the
-- whole account's listing, that reports it.
ALTER TABLE repositories ADD COLUMN visibility text NOT NULL DEFAULT '';
