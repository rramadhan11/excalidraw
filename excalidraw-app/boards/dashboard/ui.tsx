/** Small building blocks shared by the dashboard components. */
import { useEffect, useRef, useState } from "react";

/** Open state for a popover that closes on outside click and Escape. */
export const usePopover = <T extends HTMLElement = HTMLDivElement>() => {
  const [open, setOpen] = useState(false);
  const ref = useRef<T>(null);

  useEffect(() => {
    if (!open) {
      return;
    }
    const onPointerDown = (event: MouseEvent) => {
      if (ref.current && !ref.current.contains(event.target as Node)) {
        setOpen(false);
      }
    };
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === "Escape") {
        setOpen(false);
      }
    };
    document.addEventListener("mousedown", onPointerDown);
    document.addEventListener("keydown", onKeyDown);
    return () => {
      document.removeEventListener("mousedown", onPointerDown);
      document.removeEventListener("keydown", onKeyDown);
    };
  }, [open]);

  return { open, setOpen, ref };
};

/**
 * A text input that submits on Enter or blur and cancels on Escape. Submitting
 * an empty or unchanged value counts as cancelling.
 */
export const InlineInput = ({
  initialValue = "",
  placeholder,
  maxLength,
  label,
  onSubmit,
  onCancel,
}: {
  initialValue?: string;
  placeholder?: string;
  maxLength: number;
  label: string;
  onSubmit: (value: string) => void;
  onCancel: () => void;
}) => {
  const [value, setValue] = useState(initialValue);
  const doneRef = useRef(false);

  const finish = (submit: boolean) => {
    if (doneRef.current) {
      return;
    }
    doneRef.current = true;
    const trimmed = value.trim();
    if (submit && trimmed && trimmed !== initialValue) {
      onSubmit(trimmed);
    } else {
      onCancel();
    }
  };

  return (
    <input
      className="boards-dashboard__input"
      aria-label={label}
      autoFocus
      maxLength={maxLength}
      placeholder={placeholder}
      value={value}
      onChange={(event) => setValue(event.target.value)}
      onBlur={() => finish(true)}
      onKeyDown={(event) => {
        if (event.key === "Enter") {
          finish(true);
        } else if (event.key === "Escape") {
          finish(false);
        }
      }}
    />
  );
};

const relativeTime = new Intl.RelativeTimeFormat("en", { numeric: "auto" });
const UNITS: [Intl.RelativeTimeFormatUnit, number][] = [
  ["year", 365 * 24 * 3600],
  ["month", 30 * 24 * 3600],
  ["week", 7 * 24 * 3600],
  ["day", 24 * 3600],
  ["hour", 3600],
  ["minute", 60],
];

/** "7 days ago", "yesterday", "just now" */
export const formatRelative = (iso: string, now = Date.now()) => {
  const seconds = (new Date(iso).getTime() - now) / 1000;
  for (const [unit, unitSeconds] of UNITS) {
    if (Math.abs(seconds) >= unitSeconds) {
      return relativeTime.format(Math.round(seconds / unitSeconds), unit);
    }
  }
  return "just now";
};
