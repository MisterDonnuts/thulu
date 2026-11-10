// Draft and interrupt are per task: shared state followed the user into the next task
// and interrupted its agent unasked.

interface Composer {
  draft: string;
  interrupt: boolean;
}

export class TaskComposers {
  private byTask = new Map<string, Composer>();

  private get(taskID: string): Composer {
    return this.byTask.get(taskID) ?? { draft: "", interrupt: false };
  }

  private set(taskID: string, c: Composer): void {
    if (!c.draft && !c.interrupt) this.byTask.delete(taskID);
    else this.byTask.set(taskID, c);
  }

  draft(taskID: string): string {
    return this.get(taskID).draft;
  }

  setDraft(taskID: string, text: string): void {
    this.set(taskID, { ...this.get(taskID), draft: text });
  }

  interrupt(taskID: string): boolean {
    return this.get(taskID).interrupt;
  }

  setInterrupt(taskID: string, on: boolean): void {
    this.set(taskID, { ...this.get(taskID), interrupt: on });
  }
}
