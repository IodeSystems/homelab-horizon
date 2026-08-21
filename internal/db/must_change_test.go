package db

import (
	"context"
	"testing"
)

// An admin-set password must not survive first use. Age-based expiry cannot
// express this: it exempts accounts with a second factor, which is exactly the
// admin whose password most needs replacing after a console reset.
func TestRequirePasswordChange(t *testing.T) {
	ctx := context.Background()
	d := open(t)

	user, err := d.CreateUser(ctx, "carl", "", RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.SetPassword(ctx, user.ID, "temporary-one"); err != nil {
		t.Fatal(err)
	}

	must, err := d.PasswordMustChange(ctx, user.ID)
	if err != nil || must {
		t.Fatalf("a fresh password should not demand a change (must=%v err=%v)", must, err)
	}

	if err := d.RequirePasswordChange(ctx, user.ID); err != nil {
		t.Fatal(err)
	}
	if must, err := d.PasswordMustChange(ctx, user.ID); err != nil || !must {
		t.Fatalf("must=%v err=%v, want the flag set", must, err)
	}

	// Setting a new password satisfies the demand.
	if err := d.SetPassword(ctx, user.ID, "chosen-by-the-user"); err != nil {
		t.Fatal(err)
	}
	if must, err := d.PasswordMustChange(ctx, user.ID); err != nil || must {
		t.Errorf("must=%v err=%v, want the flag cleared once a new password is set", must, err)
	}
}

// An account with no password at all is not stuck demanding a change it has no
// way to satisfy.
func TestPasswordMustChangeWithoutAPassword(t *testing.T) {
	ctx := context.Background()
	d := open(t)

	user, err := d.CreateUser(ctx, "sso-only", "", RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}
	if must, err := d.PasswordMustChange(ctx, user.ID); err != nil || must {
		t.Errorf("must=%v err=%v, want false", must, err)
	}
	if err := d.RequirePasswordChange(ctx, user.ID); err == nil {
		t.Error("requiring a change on an account with no password should report that")
	}
}
