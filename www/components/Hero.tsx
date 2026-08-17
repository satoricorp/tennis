"use client";

import { useState } from "react";
import styles from "./Hero.module.css";

// One source of truth for the strings that appear in more than one place.
const REPO = "github.com/satoricorp/tennis";
const INSTALL = `go install ${REPO}/cmd/tennis@latest`;
const ADD = "tennis add --chatgpt";
const SEARCH = 'tennis search "What hotel did we stay at in Mexico last year?"';
const SESSION = `${ADD}\n${SEARCH}`;

const DOCS_URL = `https://${REPO}#readme`;
const RELEASES_URL = `https://${REPO}/releases`;
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
        <span className={styles.wordmark}>Tennis</span>

        <p className={styles.tagline}>Store all of your context in one place</p>
        <p className={styles.subtitle}>
          Tennis can collect sessions from Claude, ChatGPT, Codex, and more,
          including local files. Your context is stored locally in SQLite &amp;
          markdown.
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
            href={RELEASES_URL}
            title="Download a binary"
            aria-label="Download a binary"
          >
            <svg viewBox="0 0 24 24" fill="none" aria-hidden="true">
              <path
                d="M12 3v12m0 0 4.5-4.5M12 15l-4.5-4.5M4 17v2a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2v-2"
                stroke="currentColor"
                strokeWidth="2"
                strokeLinecap="round"
                strokeLinejoin="round"
              />
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
