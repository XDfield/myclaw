# WeChat Setup

myclaw integrates WeChat via the [corespeed-io/wechatbot](https://github.com/corespeed-io/wechatbot) Go SDK, which uses the WeChat iLink Bot API.

## Prerequisites

- A WeChat account with iLink Bot access (requires WeChat official authorization)
- myclaw installed and configured

## Configuration

Add a `wechat` section to `~/.myclaw/config.json`:

```json
{
  "channels": {
    "wechat": {
      "enabled": true,
      "credPath": "",
      "allowFrom": ["wechat_user_id_1"]
    }
  }
}
```

| Field | Description |
|-------|-------------|
| `enabled` | Enable the WeChat channel |
| `credPath` | Path to store credentials (default: `~/.wechatbot/`) |
| `allowFrom` | Allowlist of WeChat user IDs; empty means allow all |

### Environment Variables

| Variable | Description |
|----------|-------------|
| `MYCLAW_WECHAT_CRED_PATH` | Override credential storage path |

## Login

Start the gateway:

```bash
make gateway
```

On first run, a QR code will appear in the terminal. Scan it with your WeChat app to log in. Credentials are saved locally and reused on subsequent starts.

## Notes

- The WeChat iLink Bot API requires official authorization from Tencent. Personal WeChat accounts may not have access.
- Session credentials are persisted across restarts. Re-login is triggered automatically if the session expires.
- `allowFrom` uses WeChat user IDs (not display names). Leave empty to allow all users.
