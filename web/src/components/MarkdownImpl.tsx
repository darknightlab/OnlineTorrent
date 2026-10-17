import ReactMarkdown from "react-markdown"
import remarkGfm from "remark-gfm"

export default function MarkdownImpl({ children }: { children: string }) {
  return (
    <ReactMarkdown
      remarkPlugins={[remarkGfm]}
      components={{
        a: ({ node: _n, ...props }) => <a {...props} target="_blank" rel="noreferrer noopener" />,
      }}
    >
      {children}
    </ReactMarkdown>
  )
}
