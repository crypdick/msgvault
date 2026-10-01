package matrix

import (
	"context"
	"errors"
	"fmt"

	"maunium.net/go/mautrix/crypto/backup"
	"maunium.net/go/mautrix/event"
)

// Recover obtains room keys from the account's authenticated server backup.
// The supplied secret and decrypted backup key are never written to the archive.
func (c *Client) Recover(ctx context.Context, secret string) (int, error) {
	if secret == "" {
		return 0, errors.New("matrix recovery secret is empty")
	}
	machine := c.crypto.Machine()
	keyID, metadata, err := machine.SSSS.GetDefaultKeyData(ctx)
	if err != nil {
		return 0, fmt.Errorf("read Matrix secret-storage metadata: %w", err)
	}
	key, err := metadata.VerifyRecoveryKey(keyID, secret)
	if err != nil {
		key, err = metadata.VerifyPassphrase(keyID, secret)
	}
	if err != nil {
		return 0, fmt.Errorf("verify Matrix recovery key or passphrase: %w", err)
	}
	data, err := machine.SSSS.GetDecryptedAccountData(ctx, event.AccountDataMegolmBackupKey, key)
	if err != nil {
		return 0, fmt.Errorf("decrypt Matrix key-backup secret: %w", err)
	}
	backupKey, err := backup.MegolmBackupKeyFromBytes(data)
	if err != nil {
		return 0, fmt.Errorf("parse Matrix key-backup secret: %w", err)
	}
	version, err := machine.GetAndVerifyLatestKeyBackupVersion(ctx, backupKey)
	if err != nil {
		return 0, fmt.Errorf("authenticate Matrix key backup: %w", err)
	}
	keys, err := c.sdk.GetKeyBackup(ctx, version.Version)
	if err != nil {
		return 0, fmt.Errorf("download Matrix key backup: %w", err)
	}
	count := 0
	var failures []error
	for roomID, room := range keys.Rooms {
		for sessionID, session := range room.Sessions {
			if err := ctx.Err(); err != nil {
				return count, err
			}
			data, err := session.SessionData.Decrypt(backupKey)
			if err == nil {
				_, err = machine.ImportRoomKeyFromBackup(ctx, version.Version, roomID, sessionID, data)
			}
			if err != nil {
				failures = append(failures, fmt.Errorf("import Matrix room key: %w", err))
			} else {
				count++
			}
		}
	}
	return count, errors.Join(failures...)
}
