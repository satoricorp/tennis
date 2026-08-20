"use client";

import { useState } from "react";
import BallBackground from "./BallBackground";
import styles from "./Hero.module.css";

// One source of truth for the strings that appear in more than one place.
const REPO = "github.com/satoricorp/tennis";
const INSTALL = `go install ${REPO}/cmd/tennis@latest`;
const ADD = "tennis add --chatgpt ~/Downloads/chatgpt-export.zip";
const SEARCH = 'tennis search "What hotel did we stay at in Mexico last year?"';
const SESSION = `${ADD}\n${SEARCH}`;

// The docs are their own Next app deployed under the same Pages site, so this
// is an absolute path rather than a <Link>: basePath is not ours to infer here.
const DOCS_URL = "/tennis/docs";
const DISCORD_URL = "https://discord.gg/JpAggvxJJ";
const GITHUB_URL = `https://${REPO}`;

function CopyButton({ value, label }: { value: string; label: string }) {
  const [copied, setCopied] = useState(false);

  const copy = async () => {
    try {
      await navigator.clipboard.writeText(value);
      setCopied(true);
      window.setTimeout(() => setCopied(false), 1400);
    } catch {
      // clipboard is unavailable outside a secure context — fail quietly
    }
  };

  return (
    <button
      type="button"
      className={styles.copy}
      onClick={copy}
      aria-label={label}
      title={copied ? "Copied" : label}
    >
      {copied ? (
        <svg viewBox="0 0 24 24" fill="none" aria-hidden="true">
          <path
            d="M20 6L9 17l-5-5"
            stroke="currentColor"
            strokeWidth="2"
            strokeLinecap="round"
            strokeLinejoin="round"
          />
        </svg>
      ) : (
        <svg viewBox="0 0 24 24" fill="none" aria-hidden="true">
          <rect
            x="8"
            y="8"
            width="14"
            height="14"
            rx="2"
            stroke="currentColor"
            strokeWidth="2"
          />
          <path
            d="M4 16c-1.1 0-2-.9-2-2V4c0-1.1.9-2 2-2h10c1.1 0 2 .9 2 2"
            stroke="currentColor"
            strokeWidth="2"
            strokeLinecap="round"
            strokeLinejoin="round"
          />
        </svg>
      )}
    </button>
  );
}

export default function Hero() {
  return (
    <main className={styles.page}>
      <div className={styles.stack}>
        {/* The ball rides the wordmark rather than sitting in the line, so
            "Tennis" stays optically centred in the stack and the mark hangs off
            its shoulder the way a ® would. */}
        <span className={styles.mark}>
          <span className={styles.wordmark}>Tennis</span>
          <BallBackground className={styles.markBall} fontSize={3} />
        </span>

        <p className={styles.tagline}>
          Store all of your sessions, take them anywhere.
        </p>
        <p className={styles.subtitle}>
          Tennis collects context from any app or file, generates markdown
          summaries to easily review, and can be stored and used anywhere.
        </p>

        <div className={styles.block}>
          <code className={styles.line}>
            <span className={styles.prompt}>$</span> {INSTALL}
          </code>
          <CopyButton value={INSTALL} label="Copy install command" />
        </div>

        {/* The payoff block: collect once, then ask a question the way you'd
            ask a person and get it answered out of a months-old session. */}
        <div className={`${styles.block} ${styles.resultBlock}`}>
          <pre className={styles.pre}>
            <code>
              <span className={styles.prompt}>$</span> {ADD}
              {"\n"}
              <span className={styles.prompt}>$</span> tennis search{" "}
              <span className={styles.str}>
                &quot;What hotel did we stay at in Mexico last year?&quot;
              </span>
              {"\n\n"}
              <span className={styles.answerMark}>&gt;</span>{" "}
              <span className={styles.answer}>
                You stayed at Hotel Esencia in Tulum, Casa Malca…
              </span>
              {/* two spaces: the width of "> ", so this sits flush under the
                  answer text rather than under the marker */}
              {"\n  "}
              <span className={styles.nowrap}>
                <span className={styles.source}>ChatGPT</span>{" "}
                <span className={styles.date}>[2025-03-15]</span>{" "}
                <span className={styles.score}>0.0231</span>
              </span>
            </code>
          </pre>
          <CopyButton value={SESSION} label="Copy commands" />
        </div>

        <div className={styles.links}>
          <a className={styles.docs} href={DOCS_URL}>
            Documentation <span className={styles.arrow}>↗</span>
          </a>
          <a
            className={styles.iconLink}
            href={DISCORD_URL}
            title="Discord"
            aria-label="Discord"
          >
            <svg viewBox="0 0 24 24" fill="currentColor" aria-hidden="true">
              <path d="M20.317 4.3698a19.7913 19.7913 0 0 0-4.8851-1.5152.0741.0741 0 0 0-.0785.0371c-.211.3753-.4447.8648-.6083 1.2495-1.8447-.2762-3.68-.2762-5.4868 0-.1636-.3933-.4058-.8742-.6177-1.2495a.077.077 0 0 0-.0785-.037 19.7363 19.7363 0 0 0-4.8852 1.515.0699.0699 0 0 0-.0321.0277C.5334 9.0458-.319 13.5799.0992 18.0578a.0824.0824 0 0 0 .0312.0561c2.0528 1.5076 4.0413 2.4228 5.9929 3.0294a.0777.0777 0 0 0 .0842-.0276c.4616-.6304.8731-1.2952 1.226-1.9942a.076.076 0 0 0-.0416-.1057c-.6528-.2476-1.2743-.5495-1.8722-.8923a.077.077 0 0 1-.0076-.1277c.1258-.0943.2517-.1923.3718-.2914a.0743.0743 0 0 1 .0776-.0105c3.9278 1.7933 8.18 1.7933 12.0614 0a.0739.0739 0 0 1 .0785.0095c.1202.099.246.1981.3728.2924a.077.077 0 0 1-.0066.1276 12.2986 12.2986 0 0 1-1.873.8914.0766.0766 0 0 0-.0407.1067c.3604.698.7719 1.3628 1.225 1.9932a.076.076 0 0 0 .0842.0286c1.961-.6067 3.9495-1.5219 6.0023-3.0294a.077.077 0 0 0 .0313-.0552c.5004-5.177-.8382-9.6739-3.5485-13.6604a.061.061 0 0 0-.0312-.0286zM8.02 15.3312c-1.1825 0-2.1569-1.0857-2.1569-2.419 0-1.3332.9555-2.4189 2.157-2.4189 1.2108 0 2.1757 1.0952 2.1568 2.419 0 1.3332-.9555 2.4189-2.1569 2.4189zm7.9748 0c-1.1825 0-2.1569-1.0857-2.1569-2.419 0-1.3332.9554-2.4189 2.1569-2.4189 1.2108 0 2.1757 1.0952 2.1568 2.419 0 1.3332-.946 2.4189-2.1568 2.4189z" />
            </svg>
          </a>
          <a
            className={styles.iconLink}
            href={GITHUB_URL}
            title="GitHub"
            aria-label="GitHub"
          >
            <svg viewBox="0 0 24 24" fill="currentColor" aria-hidden="true">
              <path d="M12 .297c-6.63 0-12 5.373-12 12 0 5.303 3.438 9.8 8.205 11.385.6.113.82-.258.82-.577 0-.285-.01-1.04-.015-2.04-3.338.724-4.042-1.61-4.042-1.61C4.422 18.07 3.633 17.7 3.633 17.7c-1.087-.744.084-.729.084-.729 1.205.084 1.838 1.236 1.838 1.236 1.07 1.835 2.809 1.305 3.495.998.108-.776.417-1.305.76-1.605-2.665-.3-5.466-1.332-5.466-5.93 0-1.31.465-2.38 1.235-3.22-.135-.303-.54-1.523.105-3.176 0 0 1.005-.322 3.3 1.23.96-.267 1.98-.399 3-.405 1.02.006 2.04.138 3 .405 2.28-1.552 3.285-1.23 3.285-1.23.645 1.653.24 2.873.12 3.176.765.84 1.23 1.91 1.23 3.22 0 4.61-2.805 5.625-5.475 5.92.42.36.81 1.096.81 2.22 0 1.606-.015 2.896-.015 3.286 0 .315.21.69.825.57C20.565 22.092 24 17.592 24 12.297c0-6.627-5.373-12-12-12" />
            </svg>
          </a>
        </div>
      </div>

      <p className={styles.copyright}>
        © 2026 <span className={styles.dot} aria-hidden="true" /> Rhythm
        Computer Co.
      </p>
    </main>
  );
}
