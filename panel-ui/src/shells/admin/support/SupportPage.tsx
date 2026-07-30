// SupportPage — admin Support landing (M29). Matches mockup [Image #25]:
// title left, "Send Diagnostic Report" button top-right, 4 cards in a row.
import { useState } from "react";
import { Button, Card, Col, Row, Space, Tag, Typography } from "antd";
import type { ReactNode } from "react";

import {
  BookOutlined,
  BugOutlined,
  ClockCircleOutlined,
  ExportOutlined,
  FileTextOutlined,
  LifeBuoyOutlined,
} from "@icons";

import { SUPPORT_LINKS } from "../../../config/support-links";
import { useServiceInfo } from "../../../useServiceInfo";
import { DiagnosticReportModal } from "./DiagnosticReportModal";
import { CliOnlyRunbooks } from "./CliOnlyRunbooks";

interface CardSpec {
  key: string;
  icon: ReactNode;
  iconColor: string;
  title: string;
  body: string;
  cta: string;
  href: string;
  primary?: boolean;
  emergency?: boolean;
}

const CARDS: CardSpec[] = [
  {
    key: "docs",
    icon: <BookOutlined />,
    iconColor: "#cf1322",
    title: "Documentation",
    body:
      "Find answers in our docs or talk with our trained support bot. Explore setup guides, troubleshooting steps, and best practices.",
    cta: "Open Documentation",
    href: SUPPORT_LINKS.documentation,
    primary: true,
  },
  {
    key: "bug",
    icon: <BugOutlined />,
    iconColor: "#fa8c16",
    title: "Report a Bug",
    body:
      `To help us diagnose issues faster, click "Send Diagnostic Report" above to generate an encrypted report with your system info, service statuses, and recent logs. Paste it in your GitHub issue — only the Jabali team can read it.`,
    cta: "Open GitHub Issues",
    href: SUPPORT_LINKS.githubIssues,
  },
  {
    key: "paid",
    icon: <LifeBuoyOutlined />,
    iconColor: "#cf1322",
    title: "Paid Support",
    body:
      "Get professional assistance for migrations, performance tuning, and priority fixes. Plans include onboarding and dedicated support.",
    cta: "View Support Plans",
    href: SUPPORT_LINKS.paidSupport,
    primary: true,
  },
  {
    key: "emergency",
    icon: <ClockCircleOutlined />,
    iconColor: "#faad14",
    title: "Emergency Support",
    body:
      "We typically respond within 4-8 hours. For critical incidents, use Emergency Support for faster response.",
    cta: "Emergency Support",
    href: SUPPORT_LINKS.emergency,
    emergency: true,
  },
];

export const SupportPage = () => {
  const [modalOpen, setModalOpen] = useState(false);
  // JAB-179: the public demo must not publish the internal CLI recovery
  // runbook (admin command names + source-file refs).
  const { data: svcInfo } = useServiceInfo();
  const demo = !!svcInfo?.demo?.enabled;

  return (
    <div>
      <Space
        wrap
        align="center"
        style={{ width: "100%", justifyContent: "space-between", marginBottom: 16 }}
      >
        <Typography.Title level={3} style={{ margin: 0 }}>
          <LifeBuoyOutlined /> Support
        </Typography.Title>
        <Button icon={<FileTextOutlined />} onClick={() => setModalOpen(true)}>
          Send Diagnostic Report
        </Button>
      </Space>

      <Row gutter={[16, 16]}>
        {CARDS.map((c) => (
          <Col key={c.key} xs={24} sm={12} lg={6}>
            <SupportCard spec={c} />
          </Col>
        ))}
      </Row>

      {!demo && <CliOnlyRunbooks />}

      <DiagnosticReportModal
        open={modalOpen}
        onClose={() => setModalOpen(false)}
      />
    </div>
  );
};

function SupportCard({ spec }: { spec: CardSpec }) {
  const hasLink = spec.href && spec.href.length > 0;
  return (
    <Card
      style={{ height: "100%" }}
      styles={{ body: { display: "flex", flexDirection: "column", gap: 16, height: "100%" } }}
    >
      <Space align="start" size={12}>
        <span style={{ color: spec.iconColor, fontSize: 22, lineHeight: 1 }}>
          {spec.icon}
        </span>
        <Typography.Title level={5} style={{ margin: 0 }}>
          {spec.title}
        </Typography.Title>
      </Space>
      <Typography.Paragraph
        type="secondary"
        style={{ flex: 1, margin: 0 }}
      >
        {spec.body}
      </Typography.Paragraph>
      {hasLink ? (
        <Button
          icon={<ExportOutlined />}
          type={spec.primary || spec.emergency ? "primary" : "default"}
          href={spec.href}
          target="_blank"
          rel="noopener noreferrer"
          style={
            spec.emergency
              ? { background: "#FFC107", borderColor: "#FFC107", color: "#000" }
              : undefined
          }
        >
          {spec.cta}
        </Button>
      ) : (
        <Tag>Coming soon</Tag>
      )}
    </Card>
  );
}
