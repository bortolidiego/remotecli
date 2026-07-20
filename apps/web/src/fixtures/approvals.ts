export interface ApprovalFixture {
  id: string
  command: string
  cwd: string
  justification: string
  permissions: string[]
}

export const pendingApprovalFixture: ApprovalFixture = {
  id: 'fixture-approval-1',
  command: 'cat package.json',
  cwd: '/Users/diegobortoli/Desktop/apps/relay',
  justification: 'Pré-visualizar um comando local antes de permitir execução única.',
  permissions: ['read cwd', 'no network', 'no write'],
}
