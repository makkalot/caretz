## /goal needs rethinking

planner : 
  gets the prompt from user and creates a plan out of it inside state.md asks for approval if not --yes is there
  planner creates the tasks for the plan and save them inside state.md and as well as inside the internal tasks system.
  planner also sets the validation criteria for the tasks. he just creates the validation criteria in natural language it's the task of the verifier to run the commands needed for it.
  planner on subsequent runs, check what failed and replans accrodingly.

checker : 
  checks each tasks was completed successfully. basically validates the task validation criteria that was put there by the planner. it runs all the bash commands needed to achiveve it. 

judge : 
  evaluates the overall plan and tasks to determine if the solution is complete and meets the user's requirements. judge should compare user goal if the solution is complete.

max_retries = n
current_retry = 0
max_time = 120min
elapsed_time = 0

while current_retry < max_retries and elapsed_time < max_time:
  plan = read_state("plan")
  if plan is None:
    plan = create_plan(prompt)
  else:
    # things might have failed so we need to update the plan
    plan = update_plan(plan)

  save_state("plan", plan)
  tasks = create_tasks(plan)
  save_state("tasks", tasks)

  task_results = []
  for task in tasks:
    result = run_task(task)
    save_update_state("task_result", result)
    if not result:
      # 1 time maybe
      retry_task(task)
    else:
      task_results.append(result)

  # after we have the task results
  save_state("task_results", task_results)
  # run the checker on the task results
  checker_results = run_checker(task_results)
  if checker_results.failures:
    save_state("checker_results", checker_results)
    set_state("replan", True)
    continue
    
  save_state("checker_results", checker_results)

  # run the judge on the checker results
  judge_results = run_judge(checker_results, plan, goal)
  save_state("judge_results", judge_results)
  if judge_results.failures:
    set_state("replan", True)
    continue

  # at this point we have a successful plan
  set_state("success", True)
  break
