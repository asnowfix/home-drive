# Application Terms of Service

**Application:** homedrive, a Google Drive sync agent
**Effective date:** 2026-11-15
**Source code and issues:** https://github.com/asnowfix/home-drive

## 1. Licence

homedrive is free software released under the MIT License. The licence text is
in the `LICENSE` file of the repository:
https://github.com/asnowfix/home-drive/blob/main/LICENSE
Using, copying or modifying homedrive means you accept that licence.

## 2. No warranty

The licence states: "THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY
KIND, EXPRESS OR IMPLIED." The maintainer gives no warranty and accepts no
liability for loss or damage arising from its use.

## 3. You run it

You install, configure and operate homedrive yourself, on hardware you control.
You are responsible for your Google account, your OAuth client, your
rclone.conf, and the local files that homedrive syncs.

## 4. OAuth client

homedrive does not include an OAuth client. You supply one through rclone.conf:
either a client you created yourself, or one you have chosen to use that was
registered by someone else. If you register an OAuth client, you are its
developer and must follow Google's API Services User Data Policy and Google's
other terms for it.

## 5. Your data and backups

homedrive can create, overwrite and delete files, both locally and in Google
Drive, as part of keeping the two in sync. When both sides change, the default
`newer_wins` policy keeps the older version with a `.old.<N>` suffix.
Deletions sent to Google Drive go to the Drive trash by default. homedrive is not a backup service. Keep your own
backups of anything you cannot afford to lose.

## 6. Support

There is no support obligation. Bug reports and questions are handled as time
allows through GitHub issues: https://github.com/asnowfix/home-drive/issues

## 7. Not affiliated with Google

homedrive is an independent project. It is not made, endorsed or supported by
Google. Use of Google Drive and of Google APIs is subject to Google's own terms.

## 8. Changes

These terms may change. The current version is in the repository, and the
effective date above is updated when they change.
