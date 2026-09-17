import { Typography } from "antd";

// Shared allowed-command help for the cron editors — tenant CreateCronModal and
// admin AdminCreateCronModal render it under the command field. Single source of
// truth for the command restrictions so the two drawers can't drift (they did:
// GH #1686 item 3, where the admin drawer showed no restriction copy at all and
// a tenant/root user had no way to know why `ls` was rejected).
//
// The wording tracks internal/cronvalidate: ValidateCommand's first-token
// allow-list (wp / php / php<X.Y> / python[3][.Y] / node, each running an
// absolute owned script — .php in a docroot, .py/.js under docroots-or-home via
// scriptRoots) plus the curl/wget self-domain http-trigger route
// (ValidateAny -> ValidateHTTPTrigger, gated on the account's own domains).
export const CronCommandHelp = () => (
  <Typography.Text type="secondary">
    Commands must start with <code>wp</code>, <code>php</code> (or a pinned
    version like <code>php8.5</code>), <code>python</code>/<code>python3</code>/
    <code>python3.X</code>, or <code>node</code>. The interpreter then runs an
    absolute script your account owns (<code>.php</code> in a docroot;{" "}
    <code>.py</code>/<code>.js</code> in a docroot or under your home
    directory). Inline code and shell operators (<code>|</code> <code>&amp;</code>{" "}
    <code>$</code> <code>;</code> …) are rejected, so plain commands like{" "}
    <code>ls</code>, <code>uptime</code>, or <code>cd … &amp;&amp; …</code> will
    not work. <code>curl</code>/<code>wget</code> are allowed only to ping one of
    your own domains.
  </Typography.Text>
);
