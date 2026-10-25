# go-groupme

A Go client for GroupMe personal-account APIs, built with
[`omniaura/go-kit`](https://github.com/omniaura/go-kit) `net/hit` and `errs`.
Includes documented v3 calls and typed implementations of undocumented web-client
features. Unofficial; not affiliated with or endorsed by GroupMe or Microsoft.

## Install

Requires Go 1.27.1 or later, matching the generic-method support used by go-kit.

```sh
go get github.com/omniaura/go-groupme@v0.2.0
```

The package name is `groupme`:

```go
import groupme "github.com/omniaura/go-groupme"

client := groupme.New(token)
page, err := client.Messages(ctx, groupID, "", 100)
if err != nil {
    return err
}
// Display page.Messages. Fetch another page only when the caller requests it.
if page.HasMore {
    nextBeforeID := page.NextBeforeID
    _ = nextBeforeID
}
```

Obtain your own access token through GroupMe's supported authentication flow.
Pass it to `New`; the client sends it in a header. Never commit tokens or include
these credentials in URLs, logs, issues, or example fixtures.

Use `NewWithTransport(token, transport)` for a caller-configured
`http.RoundTripper`, such as a proxy or tracing transport. Each client keeps its
own HTTP configuration; the SDK still controls fixed service URLs, timeouts,
response limits, redirect refusal and its no-retry policy.

Gallery pagination uses timestamps rather than message IDs:

```go
media, err := client.GalleryPage(ctx, conversationID, groupme.GalleryOptions{
    Limit: 100,
    Before: beforeTimestamp, // Empty for the first page.
})
if err != nil {
    return err
}
// Only a separate, deliberate call follows media.NextBeforeTimestamp.
```

`GalleryPage` validates provider timestamps and conversation identity. A full
page provides a continuation hint, not a guarantee that another page exists.
The older `Gallery` method remains a first-page compatibility wrapper.
Timestamp cursors cannot disambiguate rows with equal `gallery_ts` values, so
this provider contract does not guarantee exhaustive traversal across ties.

## API coverage

| Area | Client methods and behavior | API status |
| --- | --- | --- |
| Account and discovery | `Me`, `Groups`, `Group`, `Chats` | Documented v3 core |
| History | `Messages`, `MessagesAfter`, `DirectMessages`, bounded `Search` | v3 core plus provider-specific wire handling |
| Messaging | `Send`, `SendDirect`, replies and mentions in `SendInput` | Documented core plus extended attachments |
| Reactions and pins | `React`, `Unlike`, `PinMessage`, `UnpinMessage`, `PinnedMessages` | Basic likes documented; rich reactions and pins undocumented |
| Selected messages | `GroupMessage`, `DirectMessage`, `EditMessage`, `DeleteMessage` | Undocumented v3/v4 methods |
| Polls | List, read, create, vote | Undocumented |
| Calendar | List/window, read, create, RSVP | Undocumented |
| Albums and gallery | List, read media pages, create, add media | Undocumented |
| Media | Image/file/video upload, staged video preparation, file metadata and status | Public image service plus undocumented media/file services |
| Custom emoji | Snapshot of 549 picker labels, pack IDs and indices | Observed 2026-10-23; not an exhaustive future catalog |

Undocumented endpoints may change without notice. A method's presence does not
promise availability for every account, conversation type, or server version.
The included tests exercise synthetic HTTP contracts and failure boundaries;
they do not establish complete live feature parity. No realtime/WebSocket client
or bot API is included.

For a caller that has an appropriate browser user agent, `WithBrowserUserAgent`
returns an independent client configuration. That value is used only for the
web-style paths that need it; it does not replace the neutral identity profile.
No browser cookies or browser session extraction are performed by this package.

## Delivery and ownership

- Message page calls return one bounded page. The client never imports all chat
  history in the background. `Search` requires `pages == 1`; use its returned
  `NextBeforeID` in a separate explicit call to continue into older history.
- Mutations are never automatically retried. A network failure may leave an
  uncertain result; inspect canonical state before deciding what to do next.
  A source GUID is not a durable exactly-once guarantee.
- Errors use go-kit `errs` factories, including `ErrAuthentication`,
  `ErrForbidden`, `ErrRejected`, `ErrRateLimited` and `ErrUncertain`. Provider
  bodies and credentials are excluded from returned errors.
- Media URLs are constrained to the expected provider origins and validated
  paths. Upload grants are scoped, response bodies and uploads are bounded, and
  credential-bearing redirects are refused. The 50 MiB client ceiling is not a
  claim about GroupMe's maximum accepted upload size.
- The application owns account authorization, user intent/approval, rate pacing,
  retention, encrypted credential storage, durable write receipts and recovery.
  This library has no database or Ditto dependency.

## Development

```sh
go mod download
go tool xgo test -count=1 ./...
go vet ./...
```

One spool-failure test uses xgo patch instrumentation, so run the full suite with
the pinned `go tool xgo` command above. HTTP tests use synthetic tokens and fake
transports or loopback servers. No GroupMe credentials or live account are needed.

## References and other clients

- [Official GroupMe API](https://dev.groupme.com/docs/v3)
- [OpenGM community docs](https://groupme-js.github.io/GroupMeCommunityDocs/),
  including undocumented endpoint descriptions
- [Beeper's GroupMe bridge](https://github.com/beeper/groupme)
- [beeper/groupme-lib](https://github.com/beeper/groupme-lib), the bridge's Go SDK
- [goddtriffin/groupme](https://github.com/goddtriffin/groupme), another Go client
- [nhomble/groupme.go](https://github.com/nhomble/groupme.go), another Go client

These are related projects, not dependencies or claims of compatible behavior.
This library was extracted from Ditto's GroupMe client; their code was not copied.
GroupMe's [API terms](https://dev.groupme.com/terms) apply independently of the
license for this library.

## License

[MIT](LICENSE), copyright Omni Aura LLC.
