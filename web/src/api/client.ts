export class RequestError extends Error {
  constructor(message: string, readonly status: number, readonly loginURL?: string) {
    super(message)
  }
}

export async function request<T>(path: string, value?: unknown): Promise<T> {
  const response = await fetch(path, value === undefined ? undefined : {
    method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(value),
  })
  if (!response.ok) {
    const detail = await response.json().catch(() => null)
    throw new RequestError(detail?.error ?? '工作台暂时不可用，请稍后重试。', response.status, detail?.login_url)
  }
  if (response.status === 204) return undefined as T
  return response.json() as Promise<T>
}

export const explain = (error: unknown) => error instanceof Error ? error.message : '请求失败，请稍后重试。'

export function encodeWorkbook(file: File): Promise<{name: string; content_base64: string}> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader()
    reader.onload = () => {
      if (typeof reader.result !== 'string') {
        reject(new Error('工作簿读取失败。'))
        return
      }
      resolve({ name: file.name, content_base64: reader.result.split(',', 2)[1] })
    }
    reader.onerror = () => reject(new Error('工作簿读取失败。'))
    reader.readAsDataURL(file)
  })
}
