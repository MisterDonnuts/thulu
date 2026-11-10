import { describe, expect, it } from "vitest";

import { firstOwnerCommand, loginNotice, noticeText } from "./login-notice";

describe("loginNotice", () => {
  it("reads the pending-access code the SSO callback adds", () => {
    const search =
      "?error=your+account+has+no+workspace+yet+%E2%80%94+ask+an+administrator+to+add+you&reason=pending_access";
    expect(loginNotice(search)).toMatchObject({
      pending: true,
      first: false,
      error: "your account has no workspace yet — ask an administrator to add you",
    });
  });

  it("treats any other refusal as an error to show", () => {
    expect(loginNotice("?error=token+exchange+failed")).toMatchObject({
      pending: false,
      notInvited: false,
      error: "token exchange failed",
    });
  });

  it("decodes once, so a literal percent sign survives", () => {
    expect(loginNotice("?error=100%25+sure").error).toBe("100% sure");
  });

  it("is empty on a plain visit", () => {
    expect(loginNotice("")).toMatchObject({ pending: false, error: "", reason: "", invited: [] });
  });

  it("reads the install's first person as pending too, with the workspace to claim", () => {
    expect(
      loginNotice(
        "?reason=pending_access_first&email=ana%40example.org&workspace=a1b2c3d4e5f60718",
      ),
    ).toMatchObject({
      pending: true,
      first: true,
      email: "ana@example.org",
      workspace: "a1b2c3d4e5f60718",
    });
    expect(loginNotice("?reason=pending_access_first&email=ana%40example.org").workspace).toBe("");
  });

  it("reads a refusal by the allowlist with its request token and invitations", () => {
    const n = loginNotice(
      "?reason=not_invited&email=cy%40example.org&request=tok.sig&invited=Acme+Web&invited=Beta",
    );
    expect(n).toMatchObject({
      notInvited: true,
      email: "cy@example.org",
      request: "tok.sig",
      invited: ["Acme Web", "Beta"],
    });
  });

  it("reads an invitation link", () => {
    expect(loginNotice("?invite=ab12").invite).toBe("ab12");
  });
});

describe("noticeText", () => {
  it("says a slow sign-in took too long instead of naming the state", () => {
    expect(noticeText(loginNotice("?reason=state_expired&error=invalid+state"))).toBe(
      "The sign-in took too long. Try again.",
    );
  });

  it("names the provider where the email needs verifying", () => {
    expect(noticeText(loginNotice("?reason=email_unverified&provider=Example+ID&error=x"))).toBe(
      "Verify your email in Example ID, then sign in again.",
    );
  });

  it("falls back to the daemon's message", () => {
    expect(noticeText(loginNotice("?error=token+exchange+failed"))).toBe("token exchange failed");
  });
});

describe("firstOwnerCommand", () => {
  it("invites the email as owner into the named workspace with the key the wizard printed", () => {
    const cmd = firstOwnerCommand("https://thulu.acme.io", "a1b2c3d4e5f60718", "ana@example.org");
    expect(cmd).toContain(
      'curl -X POST "https://thulu.acme.io/auth/workspaces/a1b2c3d4e5f60718/members"',
    );
    expect(cmd).not.toContain("$OCTOPUS_OPERATOR_WORKSPACE");
    expect(cmd).toContain("Bearer $OCTOPUS_API_KEY");
    expect(cmd).toContain(`-d '{"email":"ana@example.org","role":"owner"}'`);
  });

  it("falls back to the env var when the daemon named no workspace", () => {
    expect(firstOwnerCommand("https://thulu.acme.io", "", "ana@example.org")).toContain(
      '"https://thulu.acme.io/auth/workspaces/$OCTOPUS_OPERATOR_WORKSPACE/members"',
    );
  });

  it("keeps a workspace id from breaking out of the quotes", () => {
    expect(firstOwnerCommand("", 'x"$(id)', "a@example.org")).toContain(
      '"/auth/workspaces/x%22%24(id)/members"',
    );
  });

  it("quotes an apostrophe in the email for the shell", () => {
    expect(firstOwnerCommand("", "ws1", "o'neil@example.org")).toContain(
      `"o'\\''neil@example.org"`,
    );
  });
});
