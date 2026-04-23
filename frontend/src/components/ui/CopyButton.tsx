import { Copy } from "lucide-react";
import toast from "react-hot-toast";
import Button from "@/components/ui/Button";

export default function CopyButton({ value, label = "Copy" }: { value: string; label?: string }) {
  return (
    <Button
      variant="ghost"
      onClick={async () => {
        await navigator.clipboard.writeText(value);
        toast.success("Copied to clipboard.");
      }}
      className="gap-2"
      type="button"
    >
      <Copy className="h-4 w-4" />
      {label}
    </Button>
  );
}
