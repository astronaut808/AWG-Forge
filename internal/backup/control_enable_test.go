package backup

import (
	"context"
	"encoding/base32"
	"path/filepath"
	"testing"
	"time"

	"github.com/astronaut808/awg-forge/internal/app"
	"github.com/astronaut808/awg-forge/internal/controlauth"
	"github.com/astronaut808/awg-forge/internal/sqldb"
	"github.com/pquerna/otp/totp"
)

func TestPrepareControlEnableCreatesVerifiedArchiveForRecentAdmin(t *testing.T) {
	ctx := context.Background()
	cfg, _ := controllerBackupFixture(t, false)
	service := app.New(cfg)
	if _, err := service.PrepareControlIdentity(ctx, app.ControlIdentityRequest{BindIP: "127.0.0.1", Advertised: "127.0.0.1", Port: 8443}); err != nil {
		t.Fatal(err)
	}
	db, err := sqldb.Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	keys, err := controlauth.LoadKeys(filepath.Join(cfg.ConfigDir, controlauth.KeyFileName))
	if err != nil {
		t.Fatal(err)
	}
	auth, err := controlauth.NewService(db, keys, controlauth.Options{})
	if err != nil {
		t.Fatal(err)
	}
	secret := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString([]byte("controller-fault-fixture-secret"))
	now := time.Now().UTC()
	code, err := totp.GenerateCode(secret, now)
	if err != nil {
		t.Fatal(err)
	}
	authentication, err := auth.Authenticate(ctx, "admin", testPassword, code, "127.0.0.1", now)
	if err != nil {
		t.Fatal(err)
	}
	archive, receipt, err := PrepareControlEnable(ctx, cfg, service, authentication.Token, testPassword)
	if err != nil {
		t.Fatal(err)
	}
	if receipt == nil || len(archive.Data) == 0 {
		t.Fatal("prepared control enable did not return an archive and receipt")
	}
	if _, err := Verify(ctx, cfg, testPassword, writeTempArchive(t, archive.Data)); err != nil {
		t.Fatalf("verify enable backup: %v", err)
	}
	for _, test := range []struct {
		name     string
		token    string
		password string
	}{
		{name: "no recent auth", password: testPassword},
		{name: "invalid backup password", token: authentication.Token, password: "short"},
	} {
		t.Run(test.name, func(t *testing.T) {
			archive, receipt, err := PrepareControlEnable(ctx, cfg, service, test.token, test.password)
			if err == nil || receipt != nil || len(archive.Data) != 0 {
				t.Fatalf("archive bytes=%d; receipt present=%v; error=%v", len(archive.Data), receipt != nil, err)
			}
		})
	}
}
