<p align="right"><b>English</b> · <a href="SECURITY.zh-CN.md">简体中文</a></p>

# Security Policy

Rigdepot edits the configuration of other tools on people's computers and, on the server, holds a team's personal secrets in encrypted form. We take reports about its security seriously, and we would much rather hear about a problem that turns out to be harmless than miss a real one.

## Reporting a vulnerability

**Please do not open a public issue, pull request or Discord message for a security problem.**

Send an email to **<rigdepot@code2rich.com>**. English and Chinese are both fine.

It helps if the report includes:

- What is affected: `rgd`, `rgd-server`, an install script, a release artifact or the container image, and which version or commit.
- What an attacker could do, and what they need in order to do it.
- Steps to reproduce, or a proof of concept. Please use a temporary home directory and made-up data; never send us real credentials or other people's data.
- Whether you plan to publish, and when.

If you do not want to put details into the first email, send a short note and we will agree on how to exchange them.

## What to expect

| Step | When |
| --- | --- |
| We confirm that we received your report | Within 3 working days |
| A first assessment: whether we can reproduce it and how serious we think it is | Within 7 days of the report |
| A fix or a mitigation is released | We aim for 90 days at most; sooner for anything being actively exploited |
| Public disclosure | Together with the fix, at a time agreed with you |

We will keep you informed while we work on it, credit you in the release notes if you would like that, and not take legal action against research done in good faith: testing on your own installation, not reading or changing other people's data, and giving us reasonable time before publishing.

## Supported versions

No version has been released yet. The table below applies from the first release.

| Version | Security fixes |
| --- | --- |
| The latest minor release (`0.Y.x`) | Yes |
| Older releases | No; please upgrade |
| Unreleased code on the `main` branch | Fixed on `main`; reports are welcome |

Before 1.0, a fix is released as a new patch version of the latest minor release. The server supports the command-line program of the previous minor release, so a security upgrade of the server does not force every laptop to upgrade the same day.

## What we especially want to hear about

- `rgd` writing, changing or deleting anything it did not record in its manifest, or overwriting a value the user set themselves.
- A personal secret reaching a place where an AI tool, a config file, a log or the server's API could show it.
- A resource bundle or repository that makes `rgd` write outside the locations it announced: path traversal, symbolic links, special file names.
- Anything in `rgd-server` that lets someone see or do what their role and scope do not allow, or that changes or drops audit records.
- A way to make the install scripts accept a file that does not match its published checksum, including the copies a server hands out with its own address and checksums filled in.
- Taking over a server that has not finished its setup without the setup token, signing a computer in without the person confirming the code in their browser, or using a device token or access token beyond what it was issued for.
- Encrypted data on the server (secrets of identity providers, repository tokens, the mail password) that can be read without the master key, or a master key that ends up in a backup, a log or an API response.
- A request from another site that the server acts on with a person's browser session.

## Verifying a release

Each release comes with a `checksums.txt` file holding the SHA-256 of every artifact. The install scripts verify the checksum and stop if it does not match.

The checksum file itself will be signed. The public key used for signing releases will be published in this file before the first release.
