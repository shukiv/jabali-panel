# Spec: `/abuse` page for jabali-panel.com

Handoff for the jabali-panel.com website developer. Ship at
**`https://jabali-panel.com/abuse`** — exact path, no redirect elsewhere, no
login, no JS required to read it. Plain HTML/Markdown is fine.

## Why this page must exist

We submitted `jabalihosted.com` to the Public Suffix List
([publicsuffix/list PR #3127](https://github.com/publicsuffix/list/pull/3127)).
`jabalihosted.com` is the base domain of Jabali Panel's free automatic-hostname
service: every server installation gets a hostname like
`192-0-2-7.jabalihosted.com` (same model as cPanel's `cprapid.com`).

The PSL submission **publicly attests** that an abuse contact is "available and
easily accessible" at this URL. PSL maintainers will visit it during review —
the PR stalls until the page is live. It must then stay up for as long as the
PSL entry exists.

## Hard requirements (from the PSL template)

1. Reachable at a stable URL: `https://jabali-panel.com/abuse`.
2. Contains an abuse-reporting contact: an email address, a form, or both.
3. Easily accessible: also add an "Abuse" link in the site footer so a visitor
   who lands on jabali-panel.com can find it without knowing the path.
4. No barriers: no login, no captcha before the contact info is visible.

## Contact address

Use **`abuse@jabali-panel.com`**. Ops note (not for the page): this mailbox is
being set up alongside `psl@jabali-panel.com`; both must be monitored — the PSL
listing carries a 30-day response commitment. If the mailbox name changes, the
page must change with it.

## Suggested copy (ready to paste, edit freely)

---

### Report abuse

Jabali Panel operates the `jabalihosted.com` hostname service, which provides
automatically assigned hostnames (such as `192-0-2-7.jabalihosted.com`) to
servers running the Jabali Panel software. Each hostname belongs to an
independent server operator — not to Jabali Panel itself.

If you have found phishing, malware, spam, or other malicious activity on a
`jabalihosted.com` hostname — or on any Jabali Panel infrastructure — please
report it to:

**abuse@jabali-panel.com**

Please include:

- the full hostname or URL involved (e.g. `192-0-2-7.jabalihosted.com`),
- what you observed (phishing page, malware download, spam source, etc.),
- when you observed it (date and timezone),
- any supporting evidence: screenshots, email headers, log excerpts.

We review every report. Confirmed abuse results in suspension or revocation of
the hostname. Reports concerning a customer's own website content should also
go to the hosting server's operator where known; `jabalihosted.com` hostnames
identify self-hosted servers we do not operate, but we can and do revoke the
hostname and its certificates when abuse is confirmed.

For non-abuse matters (support, sales, security vulnerability disclosure), see
[jabali-panel.com](https://jabali-panel.com/).

---

## Nice-to-have (not required for PSL)

- A minimal report form posting to the same mailbox (name optional, URL,
  description). Email alone satisfies the requirement.
- A line linking to a security.txt (`/.well-known/security.txt`) if the site
  ever ships one — different audience (vulnerability reporters), same spirit.

## Acceptance check

`curl -s https://jabali-panel.com/abuse | grep -i abuse@` returns the address,
and the footer of the homepage links to `/abuse`. Tell Shuki when it's live so
the PSL reviewers find it on first look.
