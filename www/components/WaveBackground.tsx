"use client";

import { useEffect, useRef } from "react";
import { asciiBackground } from "asciify-engine";

/* The ASCII wave field from the topo site, carried over so the two properties
   read as one house. The engine options below are topo's exactly. What differs
   is the frame: topo runs the wave inside a rounded card, and here it is the
   whole page behind centred text — so the accommodation is made in CSS, where
   .wave::after clears the field back to paper under the column. */
export default function WaveBackground({ className }: { className?: string }) {
  const ref = useRef<HTMLDivElement>(null);

  useEffect(() => {
    const host = ref.current;
    if (!host) {
      return;
    }

    // An animated field is exactly what "reduce motion" is asking us not to
    // draw, and the page is complete without it.
    if (window.matchMedia("(prefers-reduced-motion: reduce)").matches) {
      return;
    }

    const { destroy } = asciiBackground(host, {
      type: "wave",
      colorScheme: "light",
      lightMode: true,
      // --faint, the same grey the prompts and icons already use
      color: "#8a8a8a",
      accentColor: "#8a8a8a",
      fontSize: 8,
      accentThreshold: 0.1,
      speed: 3.0,
      mouseInfluence: 0.25,
      opacity: 1,
      zIndex: 0,
    });

    return destroy;
  }, []);

  return <div ref={ref} className={className} aria-hidden="true" />;
}
